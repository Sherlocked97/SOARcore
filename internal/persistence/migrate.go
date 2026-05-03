package persistence

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// pgx5 driver lets golang-migrate use the same pgx/v5 stack we use
	// elsewhere — no second connection library in the binary. The blank
	// import (`_ "..."`) means "load this package for its init() side
	// effects" — it registers itself with golang-migrate's driver
	// registry without us referencing any of its identifiers. The driver
	// registers itself under the URL scheme "pgx5".
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrateSchemeFromDSN converts a pgxpool-style DSN ("postgres://..." or
// "postgresql://...") to the scheme golang-migrate's pgx5 driver expects
// ("pgx5://..."). pgxpool and the migrate driver share the rest of the
// URL format, so this is a one-line swap.
func migrateSchemeFromDSN(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "postgres://"):
		return "pgx5://" + strings.TrimPrefix(dsn, "postgres://")
	case strings.HasPrefix(dsn, "postgresql://"):
		return "pgx5://" + strings.TrimPrefix(dsn, "postgresql://")
	default:
		return dsn // already prefixed, or some other scheme
	}
}

// RunMigrations applies any pending migrations. It is idempotent — if no
// migrations are pending it returns nil. It is safe to call on every
// service start; production deployments may prefer to run it as a
// separate one-shot job, which is why we expose it as its own function.
//
// `migrationsFS` carries the SQL files. Callers obtain it via go:embed
// in their main.go (the //go:embed directive must live in the package
// that owns the embedded files, and we want migrations/ at the repo
// root for tooling reasons — so the embed lives in cmd/core/main.go).
//
// `subdir` is the sub-path within migrationsFS that holds the .sql files
// (e.g. "migrations").
//
// The dsn must use the pgx5 scheme — but golang-migrate's pgx5 driver
// accepts the same `postgres://...` URLs that pgxpool does, so callers
// can pass the same DSN they hand to NewPool.
func RunMigrations(migrationsFS fs.FS, subdir string, dsn string, logger *slog.Logger) error {
	src, err := iofs.New(migrationsFS, subdir)
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}

	// migrate.NewWithSourceInstance opens the database connection itself
	// based on the DSN scheme. We pass "iofs" as the source name purely
	// for log lines. The DSN scheme must match a registered driver,
	// hence the swap to pgx5://.
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateSchemeFromDSN(dsn))
	if err != nil {
		return fmt.Errorf("new migrate: %w", err)
	}
	defer func() {
		// migrate's Close returns two errors (source + database); we
		// log them but don't propagate, since the migration itself has
		// already succeeded or failed by this point.
		serr, derr := m.Close()
		if serr != nil {
			logger.Warn("migrate source close", "err", serr)
		}
		if derr != nil {
			logger.Warn("migrate database close", "err", derr)
		}
	}()

	// Up runs all pending migrations. ErrNoChange is returned when there
	// is nothing to do — explicitly not an error.
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	logger.Info("migrations applied")
	return nil
}

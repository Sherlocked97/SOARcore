// Package persistence is the only place in the codebase that imports the
// Postgres driver or writes SQL. Every other module talks to it through
// repository types defined here. That boundary is enforced by code review,
// not by Go's type system — the rule is "if you grep for `pgx.` and you're
// not in this package, that's a bug".
//
// Why it's structured this way
// ----------------------------
// ADR-001 picks Postgres as the system-of-record and requires that state
// changes plus their audit row plus their outbox row all happen in a
// single transaction. Concentrating DB code here means the
// transaction-management primitives live in one file (this one) and the
// per-entity SQL lives next to it (incidents.go, audit.go, outbox.go,
// connectors.go). Service-layer code (in internal/domain/...) calls
// WithTx() to get a transaction, hands the *pgx.Tx to each repo method,
// and either commits or rolls back. The repos never open transactions of
// their own.
package persistence

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"         // bare driver, used for *pgx.Tx and pgx.ErrNoRows.
	"github.com/jackc/pgx/v5/pgconn"  // pgconn.CommandTag is the result type returned by Exec.
	"github.com/jackc/pgx/v5/pgxpool" // connection pool — the canonical entry point.
)

// Pool is the connection pool. Every repository takes a *Pool (or a
// *pgx.Tx within a transaction). It's a thin wrapper around pgxpool.Pool
// — we keep the wrapper in case we need to add cross-cutting concerns
// later (metrics, tracing) without changing every call site.
type Pool struct {
	pgx    *pgxpool.Pool
	logger *slog.Logger
}

// NewPool dials Postgres and verifies connectivity with a quick ping. The
// caller is responsible for calling Close() on shutdown.
func NewPool(ctx context.Context, dsn string, logger *slog.Logger) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pgx config: %w", err)
	}
	// Conservative defaults; tunable later if needed.
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute

	pgxPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("new pgx pool: %w", err)
	}
	if err := pgxPool.Ping(ctx); err != nil {
		pgxPool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Pool{pgx: pgxPool, logger: logger}, nil
}

// Close releases all connections in the pool.
func (p *Pool) Close() {
	if p.pgx != nil {
		p.pgx.Close()
	}
}

// WithTx runs `fn` inside a database transaction. If fn returns nil, the
// transaction is committed; if it returns an error, the transaction is
// rolled back and the error is returned. This is the single primitive the
// service layer uses to hold the "incident + audit + outbox in one tx"
// invariant from ADR-001.
//
// Errors from Commit/Rollback themselves are wrapped so the caller can
// distinguish "the work failed" from "the commit failed".
func (p *Pool) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := p.pgx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// defer runs when this function returns. If fn returns an error, we
	// rollback; if fn returns nil, we'll have already committed below
	// and rollback becomes a no-op.
	defer func() {
		// Best-effort rollback; logging the error is enough — the
		// caller already gets the outer error.
		if rerr := tx.Rollback(ctx); rerr != nil && rerr != pgx.ErrTxClosed {
			p.logger.Warn("tx rollback", "err", rerr)
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// WaitForDB is the Postgres equivalent of events.WaitForBroker — retries
// the initial connection so that container start-up races don't crash the
// service.
func WaitForDB(ctx context.Context, dsn string, attempts int, delay time.Duration, logger *slog.Logger) (*Pool, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		p, err := NewPool(ctx, dsn, logger)
		if err == nil {
			return p, nil
		}
		lastErr = err
		logger.Info("waiting for Postgres", "attempt", i+1, "of", attempts, "err", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("postgres unreachable after %d attempts: %w", attempts, lastErr)
}

// Querier is the small subset of the pgx surface that repository methods
// need. Both *pgxpool.Pool and pgx.Tx satisfy this interface, so a
// repository can be called either with the pool directly (for read-only
// queries that don't need a transaction) or inside a transaction.
//
// Defining our own interface instead of importing pgx's full one is a
// deliberate boundary: it documents *exactly* what persistence relies on
// from the driver, which makes future driver swaps (e.g. to a connection
// proxy in tests) tractable.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pgx returns the underlying pool as a Querier for read-only callers that
// don't need a transaction. Callers that *do* need a transaction should
// use WithTx.
func (p *Pool) Pgx() Querier { return p.pgx }

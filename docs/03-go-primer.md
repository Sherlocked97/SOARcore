# 03 — Go Primer (only what this codebase uses)

If Go is new to you, this page covers exactly the language features you
need to read SOARcore. It is not a substitute for the [Tour of Go]; it
*is* a translation guide so you can stop looking up syntax mid-read.

[Tour of Go]: https://go.dev/tour/

## Module + package layout

- `go.mod` declares the **module** name (the import-path prefix:
  `github.com/Sherlocked97/soarcore`) and the Go version floor.
- A **package** is one directory of `.go` files that all share the
  same `package` keyword on line 1.
- The `cmd/` convention holds runnable programs (`package main` +
  `func main()`); `internal/` is an actual Go-toolchain rule —
  packages under `internal/` cannot be imported from outside this
  module.

## Imports

```go
import (
    "context"               // stdlib: short path
    "github.com/google/uuid" // third-party: full URL
)
```

A blank import (`_ "..."`) means "load this package for its `init()`
side effects, but don't reference any of its identifiers." We use one
in `internal/persistence/migrate.go` to register a database driver.

## Identifiers and visibility

- `CamelCase` (capital first letter) → **exported** (visible outside
  the package).
- `camelCase` (lowercase first letter) → **unexported** (package-local).

There's no `private` / `public` keyword; case decides.

## Functions and methods

```go
func NewService(repo *Repo) *Service { ... }   // package-level function

func (s *Service) Create(ctx context.Context, ...) (*Incident, error) { ... }
//   ^^^ "receiver": s is the value the method runs on (a *Service)
```

Pointer receivers (`*Service`) let the method mutate the receiver.
Value receivers (`Service`) get a copy. We use pointer receivers for
anything that holds dependencies or state.

## Errors

Go has no exceptions. Functions return an `error` as the last value:

```go
got, err := repo.Get(ctx, id)
if err != nil {
    return nil, fmt.Errorf("get incident: %w", err)
    //                                       ^^ wraps so callers can errors.Is/As
}
```

Patterns you'll see:

- `errors.Is(err, ErrNotFound)` — "is this error (or one it wraps) the
  sentinel `ErrNotFound`?"
- `errors.As(err, &target)` — "unwrap to a typed error if any layer
  matches."
- `fmt.Errorf("...: %w", err)` — wrap an error with context, preserving
  the original.

## Structs, tags, and "interfaces are satisfied implicitly"

```go
type Envelope struct {
    EventID   uuid.UUID `json:"event_id"`
    EventType string    `json:"event_type"`
}
```

The backtick-quoted parts are **struct tags** read by reflection-based
libraries (here, `encoding/json`).

Interfaces:

```go
type Authorizer interface {
    Allow(ctx context.Context, action, resource string) error
}
```

A type implements `Authorizer` simply by having an `Allow` method with
the right signature — no `implements` keyword. `StubAuthorizer{}`
satisfies it because we wrote `func (StubAuthorizer) Allow(...) error`.

## Context

`context.Context` is Go's universal cancellation/deadline carrier.
Every long-running or cancellable function takes one as its first
argument. We use it for HTTP request lifetime, DB query deadlines, and
graceful shutdown.

```go
ctx, cancel := context.WithTimeout(parent, 5*time.Second)
defer cancel()
got := pool.QueryRow(ctx, "...") // ctx cancels in 5s if not finished.
```

## Goroutines and channels

`go someFunc()` runs `someFunc` concurrently — Go's lightweight
"thread", scheduled by the runtime, not the OS.

A `chan T` is a typed pipe between goroutines. We use it sparingly;
the patterns in this codebase are:

- `select { case <-ctx.Done(): ... case <-ticker.C: ... }` — wait on
  whichever happens first.
- `make(chan error, 1)` — a buffered channel used as a one-shot signal
  so a sender doesn't block.

## `defer`

`defer foo()` schedules `foo` to run when the surrounding function
returns. We use it for "always release this resource", e.g.:

```go
rows, err := q.Query(ctx, sql, args...)
if err != nil { return nil, err }
defer rows.Close()  // runs no matter how we exit.
```

## `slog` (structured logging)

The stdlib's structured logger. Calls look like:

```go
logger.Info("http request", "method", r.Method, "status", code)
```

Each pair after the message is a key/value. The handler we use writes
JSON to stdout — easy for log shippers and downstream tools to parse.

## `go:embed`

A compile-time directive that bakes files into the binary as a
filesystem (`embed.FS`). We use it for the JSON Schema and the SQL
migrations. The directive must live in the package whose source
directory contains the embedded files; that's why
`migrations/embed.go` exists.

## What we deliberately don't use

- **Generics** — none today; they'd add cognitive cost without payoff.
- **Reflection** — only what `encoding/json` does for us.
- **`init()`** — we wire things explicitly in `main()` for readability.
- **Third-party config / DI frameworks** — plain constructors.

That's the whole language surface area you'll meet in this codebase.

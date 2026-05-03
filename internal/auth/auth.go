// Package auth holds the authentication & authorization plumbing. v1
// ships a *stub* — every request is allowed — but the *shape* of the
// code is real:
//
//   - The middleware extracts a Principal (who is making the call) and a
//     tenant_id (whose data is being touched) and stuffs them into the
//     request context.
//   - The Authorizer interface exposes Allow(ctx, action, resource); the
//     v1 implementation always returns nil. Swapping it for an OPA-,
//     OIDC-, or RBAC-backed implementation is a single replacement at
//     wire-up time, with no call-site changes.
//
// Because the shape is real, the rest of the codebase can already write
// the right kind of code: handlers receive a Principal from the context,
// service methods accept it, audit rows record it. The day we plug in a
// real authorizer, every existing call site already passes the inputs.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// PrincipalType mirrors the actor types in the event envelope. Keeping
// them aligned avoids translation logic when an audit row is written.
type PrincipalType string

const (
	PrincipalUser      PrincipalType = "user"
	PrincipalConnector PrincipalType = "connector"
	PrincipalSystem    PrincipalType = "system"
)

// Principal identifies the caller of an HTTP request.
type Principal struct {
	Type PrincipalType
	ID   string
}

// String renders the principal in the canonical "type:id" form used on
// the X-Principal-Id header, in audit rows, and in event envelopes.
func (p Principal) String() string { return fmt.Sprintf("%s:%s", p.Type, p.ID) }

// ctxKey is an unexported type so consumers cannot collide with our
// context keys. The Go community convention is to define a custom type
// for context keys; using string would risk silent overwrites between
// packages.
type ctxKey int

const (
	principalCtxKey ctxKey = iota // iota auto-numbers consecutive constants.
	tenantCtxKey
)

// Authorizer decides whether a Principal may perform an action on a
// resource. v1's StubAuthorizer always returns nil; production
// implementations will use the principal, action, and resource (e.g. a
// tenant ID) to consult an RBAC policy.
type Authorizer interface {
	Allow(ctx context.Context, action string, resource string) error
}

// ErrDenied is the error every Authorizer implementation must return
// when it refuses an action. Handlers map it to 403.
var ErrDenied = errors.New("authorization denied")

// StubAuthorizer is the v1 implementation: every action is allowed. It
// satisfies the Authorizer interface (Go uses structural typing — a
// type satisfies an interface by having the right methods, no `implements`
// keyword needed).
type StubAuthorizer struct{}

// Allow on the stub always returns nil.
func (StubAuthorizer) Allow(ctx context.Context, action string, resource string) error {
	return nil
}

// Middleware returns an http.Handler that wraps `next`, extracts the
// principal + tenant from headers, and stores them in the request
// context. Handlers downstream pull them back out via PrincipalFrom and
// TenantFrom.
//
// Headers (v1, all stub):
//   - X-Principal-Id: "type:id" (e.g. "user:11111111-...", "connector:reference-connector").
//     If absent or malformed, the request is rejected with 401 — even
//     though there's no real authentication, callers must declare *who*
//     they claim to be so audit rows are meaningful.
//   - X-Tenant-Id: a UUID. Optional; if absent, defaults to defaultTenant.
func Middleware(defaultTenant uuid.UUID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pHeader := r.Header.Get("X-Principal-Id")
			if pHeader == "" {
				http.Error(w, `{"error":"missing X-Principal-Id"}`, http.StatusUnauthorized)
				return
			}
			principal, err := parsePrincipal(pHeader)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"invalid X-Principal-Id: %s"}`, err), http.StatusUnauthorized)
				return
			}

			tenant := defaultTenant
			if t := r.Header.Get("X-Tenant-Id"); t != "" {
				parsed, err := uuid.Parse(t)
				if err != nil {
					http.Error(w, `{"error":"invalid X-Tenant-Id"}`, http.StatusBadRequest)
					return
				}
				tenant = parsed
			}

			// context.WithValue threads values through the request so
			// later handlers can pull them back out without function
			// argument plumbing.
			ctx := r.Context()
			ctx = context.WithValue(ctx, principalCtxKey, principal)
			ctx = context.WithValue(ctx, tenantCtxKey, tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// parsePrincipal splits "type:id" into a typed Principal. We deliberately
// limit the accepted types to the three documented ones — typos would
// otherwise silently produce nonsense audit rows.
func parsePrincipal(s string) (Principal, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Principal{}, fmt.Errorf("expected type:id")
	}
	t := PrincipalType(parts[0])
	switch t {
	case PrincipalUser, PrincipalConnector, PrincipalSystem:
		// ok
	default:
		return Principal{}, fmt.Errorf("unknown principal type %q", parts[0])
	}
	return Principal{Type: t, ID: parts[1]}, nil
}

// PrincipalFrom returns the principal stored on the context by
// Middleware. The second return value is false if no principal is set —
// every handler that needs a principal should treat that as a 500 (the
// middleware should have rejected the request before it got this far).
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalCtxKey).(Principal)
	return p, ok
}

// TenantFrom returns the tenant_id stored on the context.
func TenantFrom(ctx context.Context) (uuid.UUID, bool) {
	t, ok := ctx.Value(tenantCtxKey).(uuid.UUID)
	return t, ok
}

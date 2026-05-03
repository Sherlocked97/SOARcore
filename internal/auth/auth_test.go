// Tests for the auth middleware. We hit the middleware as if it were
// any other http.Handler stack, and assert (a) the principal & tenant
// land on the request context, (b) malformed inputs are rejected with
// the right status, and (c) the default tenant kicks in when no header
// is supplied.
//
//	go test -run TestMiddleware -v ./internal/auth
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestMiddleware_PopulatesContext(t *testing.T) {
	defaultTenant := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	otherTenant := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	// Inner handler captures what it sees on the context so we can
	// assert against it after the round-trip.
	var gotPrincipal Principal
	var gotTenant uuid.UUID
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal, _ = PrincipalFrom(r.Context())
		gotTenant, _ = TenantFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	mw := Middleware(defaultTenant)(inner)

	cases := []struct {
		name           string
		principal      string
		tenant         string
		wantStatus     int
		wantPrincipal  Principal
		wantTenant     uuid.UUID
	}{
		{
			name:          "user principal, default tenant",
			principal:     "user:abc",
			tenant:        "",
			wantStatus:    http.StatusNoContent,
			wantPrincipal: Principal{Type: PrincipalUser, ID: "abc"},
			wantTenant:    defaultTenant,
		},
		{
			name:          "connector principal, explicit tenant",
			principal:     "connector:reference-connector",
			tenant:        otherTenant.String(),
			wantStatus:    http.StatusNoContent,
			wantPrincipal: Principal{Type: PrincipalConnector, ID: "reference-connector"},
			wantTenant:    otherTenant,
		},
		{
			name:          "system principal",
			principal:     "system:relay",
			tenant:        "",
			wantStatus:    http.StatusNoContent,
			wantPrincipal: Principal{Type: PrincipalSystem, ID: "relay"},
			wantTenant:    defaultTenant,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPrincipal = Principal{}
			gotTenant = uuid.Nil

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.principal != "" {
				req.Header.Set("X-Principal-Id", tc.principal)
			}
			if tc.tenant != "" {
				req.Header.Set("X-Tenant-Id", tc.tenant)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if gotPrincipal != tc.wantPrincipal {
				t.Fatalf("principal: got %#v, want %#v", gotPrincipal, tc.wantPrincipal)
			}
			if gotTenant != tc.wantTenant {
				t.Fatalf("tenant: got %v, want %v", gotTenant, tc.wantTenant)
			}
		})
	}
}

func TestMiddleware_RejectsBadHeaders(t *testing.T) {
	defaultTenant := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not run on rejected request")
	})
	mw := Middleware(defaultTenant)(inner)

	cases := []struct {
		name       string
		principal  string
		tenant     string
		wantStatus int
	}{
		{"missing principal", "", "", http.StatusUnauthorized},
		{"empty type", ":abc", "", http.StatusUnauthorized},
		{"empty id", "user:", "", http.StatusUnauthorized},
		{"unknown type", "alien:xyz", "", http.StatusUnauthorized},
		{"no separator", "useronly", "", http.StatusUnauthorized},
		{"bad tenant", "user:abc", "not-a-uuid", http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.principal != "" {
				req.Header.Set("X-Principal-Id", tc.principal)
			}
			if tc.tenant != "" {
				req.Header.Set("X-Tenant-Id", tc.tenant)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body)
			}
		})
	}
}

func TestStubAuthorizer_AlwaysAllows(t *testing.T) {
	a := StubAuthorizer{}
	if err := a.Allow(context.Background(), "anything", "anywhere"); err != nil {
		t.Fatalf("stub denied: %v", err)
	}
}

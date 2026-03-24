package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/identity"
)

// buildAuthClosure replicates the auth closure from main() so tests can
// exercise the actual per-route wiring without starting a real server.
func buildAuthClosure(
	idProvider identity.Provider,
	authzr *authz.Authorizer,
) func(string, http.HandlerFunc) http.HandlerFunc {
	return func(pattern string, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			principal, err := idProvider.Resolve(r)
			if err != nil {
				http.Error(w, "authentication failed: "+err.Error(), http.StatusUnauthorized)
				return
			}
			if authErr := authzr.Authorize(pattern, principal); authErr != nil {
				status := http.StatusForbidden
				if errors.Is(authErr, authz.ErrUnauthorized) {
					status = http.StatusUnauthorized
				}
				http.Error(w, authErr.Error(), status)
				return
			}
			h(w, r.WithContext(authz.WithPrincipal(r.Context(), principal)))
		}
	}
}

// newAuthStore creates a temporary SQLite store and cleans it up after the test.
func newAuthStore(t *testing.T) *audit.Store {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newEnforcingProvider writes usersYAML to a temp file and returns a StaticProvider.
func newEnforcingProvider(t *testing.T, usersYAML string) identity.Provider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.yaml")
	if err := os.WriteFile(path, []byte(usersYAML), 0600); err != nil {
		t.Fatalf("write users.yaml: %v", err)
	}
	p, err := identity.NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	return p
}

// ── ServiceOnly: human caller rejected ───────────────────────────────────────

func TestAuditdAuth_ServiceOnly_HumanRejected(t *testing.T) {
	// Alice is a human with the dba role — not a service account.
	// POST /v1/events is ServiceOnly; human callers must be rejected with 403.
	usersYAML := `
users:
  - id: alice@example.com
    roles: [dba]
`
	idProvider := newEnforcingProvider(t, usersYAML)
	authzr := authz.NewAuthorizer(authz.DefaultAuditdPermissions, true)
	auth := buildAuthClosure(idProvider, authzr)

	store := newAuthStore(t)
	srv := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", auth("POST /v1/events", srv.handleRecordEvent))

	req := httptest.NewRequest(http.MethodPost, "/v1/events",
		strings.NewReader(`{"event_type":"tool_execution","agent":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "alice@example.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("human on ServiceOnly endpoint: status = %d, want 403", rec.Code)
	}
}

// ── ServiceOnly: anonymous caller rejected ────────────────────────────────────

func TestAuditdAuth_ServiceOnly_AnonymousRejected(t *testing.T) {
	// No X-User header → anonymous principal.
	// POST /v1/events is ServiceOnly but also requires authentication, so
	// anonymous gets 401 (ErrUnauthorized) not 403.
	usersYAML := `
users:
  - id: alice@example.com
    roles: [dba]
`
	idProvider := newEnforcingProvider(t, usersYAML)
	authzr := authz.NewAuthorizer(authz.DefaultAuditdPermissions, true)
	auth := buildAuthClosure(idProvider, authzr)

	store := newAuthStore(t)
	srv := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", auth("POST /v1/events", srv.handleRecordEvent))

	req := httptest.NewRequest(http.MethodPost, "/v1/events",
		strings.NewReader(`{"event_type":"tool_execution","agent":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	// No X-User header.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous on ServiceOnly endpoint: status = %d, want 401", rec.Code)
	}
}

// ── Authenticated read: anonymous caller rejected ─────────────────────────────

func TestAuditdAuth_AuthenticatedRead_AnonymousRejected(t *testing.T) {
	// GET /v1/events requires authentication but no specific role.
	// Anonymous caller (no X-User header) must get 401.
	usersYAML := `
users:
  - id: alice@example.com
    roles: [dba]
`
	idProvider := newEnforcingProvider(t, usersYAML)
	authzr := authz.NewAuthorizer(authz.DefaultAuditdPermissions, true)
	auth := buildAuthClosure(idProvider, authzr)

	store := newAuthStore(t)
	srv := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events", auth("GET /v1/events", srv.handleQueryEvents))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	// No X-User header.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous on authenticated read endpoint: status = %d, want 401", rec.Code)
	}
}

// ── Principal propagation through auth closure ────────────────────────────────

func TestAuditdAuth_PrincipalInContext(t *testing.T) {
	// Verify that the auth closure stores the resolved principal in context so
	// downstream handlers can call authz.PrincipalFromContext.
	usersYAML := `
users:
  - id: alice@example.com
    roles: [dba]
`
	idProvider := newEnforcingProvider(t, usersYAML)
	authzr := authz.NewAuthorizer(authz.DefaultAuditdPermissions, true)
	auth := buildAuthClosure(idProvider, authzr)

	var gotUserID string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events", auth("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		gotUserID = authz.PrincipalFromContext(r.Context()).EffectiveID()
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-User", "alice@example.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if gotUserID != "alice@example.com" {
		t.Errorf("principal in context: UserID=%q, want alice@example.com", gotUserID)
	}
}

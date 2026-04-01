package authz

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/identity"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func anonPrincipal() identity.ResolvedPrincipal {
	return identity.ResolvedPrincipal{AuthMethod: "header", UserID: "anon"}
}

func authedPrincipal(roles ...string) identity.ResolvedPrincipal {
	return identity.ResolvedPrincipal{AuthMethod: "api_key", UserID: "alice@example.com", Roles: roles}
}

func servicePrincipal(name string, roles ...string) identity.ResolvedPrincipal {
	return identity.ResolvedPrincipal{AuthMethod: "api_key", Service: name, Roles: roles}
}

func adminPrincipal() identity.ResolvedPrincipal {
	return authedPrincipal("admin")
}

// ── Authorize() ───────────────────────────────────────────────────────────────

func TestAuthorize_NonEnforcing(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, false)

	cases := []struct {
		pattern   string
		principal identity.ResolvedPrincipal
	}{
		{"POST /api/v1/fleet/jobs", anonPrincipal()},
		{"POST /api/v1/db/{tool}", anonPrincipal()},
		{"GET /api/v1/query", anonPrincipal()},
		{"totally unknown", anonPrincipal()},
	}
	for _, tc := range cases {
		if err := a.Authorize(tc.pattern, tc.principal); err != nil {
			t.Errorf("non-enforcing Authorize(%q) = %v, want nil", tc.pattern, err)
		}
	}
}

func TestAuthorize_AllowAnonymous(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	routes := []string{
		"GET /health",
		"GET /api/v1/agents",
		"GET /api/v1/tools",
		"GET /api/v1/tools/{toolName}",
	}
	for _, pattern := range routes {
		if err := a.Authorize(pattern, anonPrincipal()); err != nil {
			t.Errorf("AllowAnonymous Authorize(%q, anon) = %v, want nil", pattern, err)
		}
		if err := a.Authorize(pattern, authedPrincipal("dba")); err != nil {
			t.Errorf("AllowAnonymous Authorize(%q, authed) = %v, want nil", pattern, err)
		}
	}
}

func TestAuthorize_RequireAuth_Anonymous(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	routes := []string{
		"POST /api/v1/query",
		"GET /api/v1/governance",
		"GET /api/v1/fleet/jobs",
	}
	for _, pattern := range routes {
		err := a.Authorize(pattern, anonPrincipal())
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Authorize(%q, anon) = %v, want ErrUnauthorized", pattern, err)
		}
	}
}

func TestAuthorize_RequireAuth_AnyRole(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	for _, p := range []identity.ResolvedPrincipal{
		authedPrincipal(),           // no roles
		authedPrincipal("readonly"), // arbitrary role
		authedPrincipal("dba"),
	} {
		if err := a.Authorize("POST /api/v1/query", p); err != nil {
			t.Errorf("Authorize(query, %v) = %v, want nil", p.Roles, err)
		}
	}
}

func TestAuthorize_RequireRoles_WrongRole(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	cases := []struct {
		pattern string
		p       identity.ResolvedPrincipal
	}{
		{"POST /api/v1/db/{tool}", authedPrincipal("readonly")},
		{"POST /api/v1/db/{tool}", authedPrincipal("fleet-operator")},
		{"POST /api/v1/k8s/{tool}", authedPrincipal("dba")},
		{"POST /api/v1/fleet/jobs", authedPrincipal("dba", "sre")},
	}
	for _, tc := range cases {
		err := a.Authorize(tc.pattern, tc.p)
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("Authorize(%q, roles=%v) = %v, want ErrForbidden", tc.pattern, tc.p.Roles, err)
		}
	}
}

func TestAuthorize_RequireRoles_MatchingRole(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	cases := []struct {
		pattern string
		p       identity.ResolvedPrincipal
	}{
		{"POST /api/v1/db/{tool}", authedPrincipal("dba")},
		{"POST /api/v1/db/{tool}", authedPrincipal("sre")},
		{"POST /api/v1/db/{tool}", authedPrincipal("oncall")},
		{"POST /api/v1/db/{tool}", servicePrincipal("srebot", "sre-automation")},
		{"POST /api/v1/k8s/{tool}", authedPrincipal("sre")},
		{"POST /api/v1/fleet/jobs", authedPrincipal("fleet-operator")},
	}
	for _, tc := range cases {
		if err := a.Authorize(tc.pattern, tc.p); err != nil {
			t.Errorf("Authorize(%q, roles=%v) = %v, want nil", tc.pattern, tc.p.Roles, err)
		}
	}
}

func TestAuthorize_AdminBypass(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	// Admin passes all role-required routes.
	for _, pattern := range []string{
		"POST /api/v1/db/{tool}",
		"POST /api/v1/k8s/{tool}",
		"POST /api/v1/fleet/jobs",
		"POST /api/v1/query",
	} {
		if err := a.Authorize(pattern, adminPrincipal()); err != nil {
			t.Errorf("admin Authorize(%q) = %v, want nil", pattern, err)
		}
	}
}

func TestAuthorize_AdminBypass_Disabled(t *testing.T) {
	// Custom table with AdminBypass: false.
	perms := map[string]Permission{
		"POST /secret": {RequireRoles: []string{"superuser"}, AdminBypass: false},
	}
	a := NewAuthorizer(perms, true)
	err := a.Authorize("POST /secret", adminPrincipal())
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("admin with AdminBypass=false should get ErrForbidden, got %v", err)
	}
}

func TestAuthorize_ServiceOnly_HumanRejected(t *testing.T) {
	a := NewAuthorizer(DefaultAuditdPermissions, true)

	for _, p := range []identity.ResolvedPrincipal{
		authedPrincipal("dba"),
		authedPrincipal("sre"),
		authedPrincipal(),
	} {
		err := a.Authorize("POST /v1/events", p)
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("ServiceOnly Authorize(POST /v1/events, human %v) = %v, want ErrForbidden", p.Roles, err)
		}
	}
}

func TestAuthorize_ServiceOnly_ServicePasses(t *testing.T) {
	a := NewAuthorizer(DefaultAuditdPermissions, true)

	serviceRoutes := []string{
		"POST /v1/events",
		"POST /v1/approvals",
		"POST /v1/governance/check",
		"POST /v1/fleet/jobs",
	}
	svc := servicePrincipal("srebot")
	for _, pattern := range serviceRoutes {
		if err := a.Authorize(pattern, svc); err != nil {
			t.Errorf("ServiceOnly Authorize(%q, service) = %v, want nil", pattern, err)
		}
	}
}

func TestAuthorize_ServiceOnly_AdminHumanBypasses(t *testing.T) {
	a := NewAuthorizer(DefaultAuditdPermissions, true)
	// Human with admin role and AdminBypass=true on service-only route.
	if err := a.Authorize("POST /v1/events", adminPrincipal()); err != nil {
		t.Errorf("admin human on ServiceOnly route = %v, want nil (AdminBypass)", err)
	}
}

func TestAuthorize_UnknownRoute_Anonymous(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	err := a.Authorize("GET /not-a-real-route", anonPrincipal())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown route + anon = %v, want ErrUnauthorized (fail-closed)", err)
	}
}

func TestAuthorize_UnknownRoute_Authenticated(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	// Authenticated users pass unknown routes; the mux returns 404.
	if err := a.Authorize("GET /not-a-real-route", authedPrincipal("dba")); err != nil {
		t.Errorf("unknown route + authed = %v, want nil", err)
	}
}

// ── Require() ─────────────────────────────────────────────────────────────────

func TestRequire_NonEnforcing(t *testing.T) {
	a := NewAuthorizer(nil, false)
	if err := a.Require(anonPrincipal(), "dba"); err != nil {
		t.Errorf("non-enforcing Require = %v, want nil", err)
	}
}

func TestRequire_Admin(t *testing.T) {
	a := NewAuthorizer(nil, true)
	if err := a.Require(adminPrincipal(), "dba", "sre"); err != nil {
		t.Errorf("admin Require = %v, want nil", err)
	}
}

func TestRequire_MatchingRole(t *testing.T) {
	a := NewAuthorizer(nil, true)
	if err := a.Require(authedPrincipal("dba"), "dba", "sre"); err != nil {
		t.Errorf("Require(dba) with roles=[dba,sre] = %v, want nil", err)
	}
}

func TestRequire_WrongRole(t *testing.T) {
	a := NewAuthorizer(nil, true)
	err := a.Require(authedPrincipal("developer"), "dba", "sre")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Require(developer) with roles=[dba,sre] = %v, want ErrForbidden", err)
	}
}

func TestRequire_EmptyRoles(t *testing.T) {
	a := NewAuthorizer(nil, true)
	err := a.Require(authedPrincipal("dba"))
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Require with empty roles list = %v, want ErrForbidden", err)
	}
}

// ── Middleware ─────────────────────────────────────────────────────────────────

// fakeProvider is a test identity provider that always returns the given principal.
type fakeProvider struct {
	principal identity.ResolvedPrincipal
	err       error
}

func (f *fakeProvider) Resolve(_ *http.Request) (identity.ResolvedPrincipal, error) {
	return f.principal, f.err
}

// makeTestRequest builds a fake request whose Pattern field is set (simulating
// Go 1.22 ServeMux matched pattern).
func makeTestRequest(method, path, pattern string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	// Set r.Pattern by routing through a real ServeMux.
	mux := http.NewServeMux()
	var captured *http.Request
	mux.HandleFunc(pattern, func(_ http.ResponseWriter, req *http.Request) {
		captured = req
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if captured == nil {
		// Pattern didn't match; return the original request with empty Pattern.
		return r
	}
	return captured
}

func TestMiddleware_ProviderError_ProtectedRoute_Returns401(t *testing.T) {
	// Bad credential on a protected route → 401 (from Authorize, not from Resolve).
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	provider := &fakeProvider{err: errors.New("bad token")}

	nextCalled := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	handler := a.Middleware(provider)(next)
	r := makeTestRequest("POST", "/api/v1/query", "POST /api/v1/query")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if nextCalled {
		t.Error("next should not be called when auth fails on a protected route")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMiddleware_ProviderError_PublicRoute_PassesThrough(t *testing.T) {
	// Bad credential on an AllowAnonymous route → falls through as anonymous → 200.
	// This is the probe-startup scenario: agent sends a wrong API key to
	// GET /v1/governance/info (AllowAnonymous) and should still get a valid response.
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	provider := &fakeProvider{err: errors.New("invalid API key")}

	nextCalled := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	handler := a.Middleware(provider)(next)
	r := makeTestRequest("GET", "/health", "GET /health")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !nextCalled {
		t.Error("next should be called: bad credential on AllowAnonymous route should fall through as anonymous")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestMiddleware_Unauthorized(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	provider := &fakeProvider{principal: anonPrincipal()}

	nextCalled := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	handler := a.Middleware(provider)(next)
	r := makeTestRequest("POST", "/api/v1/query", "POST /api/v1/query")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if nextCalled {
		t.Error("next should not be called for anonymous on auth-required route")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMiddleware_Forbidden(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	// User with "developer" role trying fleet-operator endpoint.
	provider := &fakeProvider{principal: authedPrincipal("developer")}

	nextCalled := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	handler := a.Middleware(provider)(next)
	r := makeTestRequest("POST", "/api/v1/fleet/jobs", "POST /api/v1/fleet/jobs")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if nextCalled {
		t.Error("next should not be called when role check fails")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestMiddleware_Pass_PrincipalInContext(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	p := authedPrincipal("fleet-operator")
	provider := &fakeProvider{principal: p}

	var gotPrincipal identity.ResolvedPrincipal
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotPrincipal = PrincipalFromContext(r.Context())
	})

	handler := a.Middleware(provider)(next)
	r := makeTestRequest("POST", "/api/v1/fleet/jobs", "POST /api/v1/fleet/jobs")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if gotPrincipal.UserID != p.UserID {
		t.Errorf("principal in context: UserID=%q, want %q", gotPrincipal.UserID, p.UserID)
	}
}

func TestMiddleware_PublicRoute_PassesAnonymous(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	provider := &fakeProvider{principal: anonPrincipal()}

	nextCalled := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	handler := a.Middleware(provider)(next)
	r := makeTestRequest("GET", "/health", "GET /health")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !nextCalled {
		t.Error("next should be called for public route with anonymous caller")
	}
}

// ── Completeness tests ─────────────────────────────────────────────────────────
// These verify that every registered route pattern has an entry in the
// permission table and vice-versa. Catches drift when routes are added or
// removed without updating the tables.

// gatewayRoutes is the authoritative list of patterns registered in
// Gateway.RegisterRoutes (cmd/gateway/gateway.go).
var gatewayRoutes = []string{
	"GET /health",
	"GET /api/v1/agents",
	"GET /api/v1/tools",
	"GET /api/v1/tools/{toolName}",
	"POST /api/v1/query",
	"POST /api/v1/incidents",
	"GET /api/v1/incidents",
	"POST /api/v1/db/{tool}",
	"POST /api/v1/k8s/{tool}",
	"POST /api/v1/research",
	"GET /api/v1/infrastructure",
	"GET /api/v1/databases",
	"GET /api/v1/governance",
	"GET /api/v1/governance/policies",
	"GET /api/v1/governance/explain",
	"GET /api/v1/governance/events",
	"GET /api/v1/governance/events/{eventID}",
	"GET /api/v1/governance/approvals/pending",
	"GET /api/v1/governance/approvals",
	"GET /api/v1/governance/verify",
	"GET /api/v1/governance/journeys",
	"GET /api/v1/governance/govbot/runs",
	"POST /api/v1/fleet/plan",
	"POST /api/v1/fleet/snapshot",
	"POST /api/v1/fleet/review",
	"POST /api/v1/fleet/jobs",
	"GET /api/v1/fleet/jobs",
	"GET /api/v1/fleet/jobs/{jobID}",
	"GET /api/v1/fleet/jobs/{jobID}/servers",
	"GET /api/v1/fleet/jobs/{jobID}/servers/{serverName}",
	"GET /api/v1/fleet/jobs/{jobID}/servers/{serverName}/steps",
	"GET /api/v1/fleet/jobs/{jobID}/approval/{approvalID}",
	"POST /api/v1/fleet/playbooks",
	"GET /api/v1/fleet/playbooks",
	"GET /api/v1/fleet/playbooks/{playbookID}",
	"PUT /api/v1/fleet/playbooks/{playbookID}",
	"DELETE /api/v1/fleet/playbooks/{playbookID}",
	"POST /api/v1/fleet/playbooks/{playbookID}/run",
	"GET /api/v1/tool-results",
	"GET /api/v1/roles",
}

// auditdRoutes is the authoritative list of patterns registered in
// cmd/auditd/main.go.
var auditdRoutes = []string{
	"POST /v1/events",
	"POST /v1/events/{eventID}/outcome",
	"GET /v1/events",
	"GET /v1/verify",
	"POST /v1/approvals",
	"GET /v1/approvals",
	"GET /v1/approvals/pending",
	"GET /v1/approvals/{approvalID}",
	"GET /v1/approvals/{approvalID}/wait",
	"POST /v1/approvals/{approvalID}/approve",
	"POST /v1/approvals/{approvalID}/deny",
	"POST /v1/approvals/{approvalID}/cancel",
	"GET /v1/governance/info",
	"GET /v1/governance/policies",
	"GET /v1/governance/explain",
	"POST /v1/governance/check",
	"GET /v1/events/{eventID}",
	"GET /v1/journeys",
	"POST /v1/govbot/runs",
	"GET /v1/govbot/runs",
	"POST /v1/fleet/jobs",
	"GET /v1/fleet/jobs",
	"GET /v1/fleet/jobs/{jobID}",
	"PATCH /v1/fleet/jobs/{jobID}/status",
	"POST /v1/fleet/jobs/{jobID}/servers",
	"PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}",
	"GET /v1/fleet/jobs/{jobID}/servers",
	"GET /v1/fleet/jobs/{jobID}/servers/{serverName}",
	"POST /v1/fleet/jobs/{jobID}/servers/{serverName}/steps",
	"PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}/steps/{stepIndex}",
	"GET /v1/fleet/jobs/{jobID}/servers/{serverName}/steps",
	"POST /v1/fleet/jobs/{jobID}/approval",
	"GET /v1/fleet/jobs/{jobID}/approval/{approvalID}",
	"POST /v1/fleet/playbooks",
	"GET /v1/fleet/playbooks",
	"GET /v1/fleet/playbooks/{playbookID}",
	"PUT /v1/fleet/playbooks/{playbookID}",
	"DELETE /v1/fleet/playbooks/{playbookID}",
	"POST /v1/tool-results",
	"GET /v1/tool-results",
	"GET /health",
	// Rollback & Undo
	"POST /v1/rollbacks",
	"GET /v1/rollbacks",
	"GET /v1/rollbacks/{rollbackID}",
	"POST /v1/rollbacks/{rollbackID}/cancel",
	"POST /v1/events/{eventID}/rollback-plan",
	"POST /v1/fleet/jobs/{jobID}/rollback",
	"GET /v1/fleet/jobs/{jobID}/rollback",
}

func TestDefaultGatewayPermissions_Completeness(t *testing.T) {
	for _, route := range gatewayRoutes {
		if _, ok := DefaultGatewayPermissions[route]; !ok {
			t.Errorf("route %q is registered in the gateway but missing from DefaultGatewayPermissions", route)
		}
	}
	for pattern := range DefaultGatewayPermissions {
		found := false
		for _, route := range gatewayRoutes {
			if route == pattern {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pattern %q is in DefaultGatewayPermissions but not registered in the gateway", pattern)
		}
	}
}

func TestDefaultAuditdPermissions_Completeness(t *testing.T) {
	for _, route := range auditdRoutes {
		if _, ok := DefaultAuditdPermissions[route]; !ok {
			t.Errorf("route %q is registered in auditd but missing from DefaultAuditdPermissions", route)
		}
	}
	for pattern := range DefaultAuditdPermissions {
		found := false
		for _, route := range auditdRoutes {
			if route == pattern {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pattern %q is in DefaultAuditdPermissions but not registered in auditd", pattern)
		}
	}
}

// ── PrincipalFromContext zero value ────────────────────────────────────────────

func TestPrincipalFromContext_ZeroValue(t *testing.T) {
	ctx := t.Context()
	p := PrincipalFromContext(ctx)
	if !p.IsAnonymous() && p.UserID != "" {
		t.Errorf("PrincipalFromContext on empty context = %+v, want zero value", p)
	}
}

// ── Auditd approve/deny role routing ──────────────────────────────────────────

func TestAuditdPermissions_ApproveRequiresRole(t *testing.T) {
	a := NewAuthorizer(DefaultAuditdPermissions, true)

	// Neither dba nor fleet-approver → 403
	err := a.Authorize("POST /v1/approvals/{approvalID}/approve", authedPrincipal("developer"))
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("developer on approve = %v, want ErrForbidden", err)
	}

	// dba → allowed
	if err := a.Authorize("POST /v1/approvals/{approvalID}/approve", authedPrincipal("dba")); err != nil {
		t.Errorf("dba on approve = %v, want nil", err)
	}

	// fleet-approver → allowed
	if err := a.Authorize("POST /v1/approvals/{approvalID}/approve", authedPrincipal("fleet-approver")); err != nil {
		t.Errorf("fleet-approver on approve = %v, want nil", err)
	}
}

func TestRequire_DynamicApprovalRole(t *testing.T) {
	a := NewAuthorizer(DefaultAuditdPermissions, true)

	// Simulate handler fine-grained check: fleet approval requires fleet-approver.
	dba := authedPrincipal("dba")
	if err := a.Require(dba, "fleet-approver"); !errors.Is(err, ErrForbidden) {
		t.Errorf("dba Require(fleet-approver) = %v, want ErrForbidden", err)
	}

	fleetApprover := authedPrincipal("fleet-approver")
	if err := a.Require(fleetApprover, "fleet-approver"); err != nil {
		t.Errorf("fleet-approver Require(fleet-approver) = %v, want nil", err)
	}
}

// ── Error message sanity ───────────────────────────────────────────────────────

func TestAuthorize_ErrorMessages(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)

	err := a.Authorize("POST /api/v1/db/{tool}", anonPrincipal())
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Errorf("anon error message = %q, want to contain 'authentication'", err)
	}

	err = a.Authorize("POST /api/v1/db/{tool}", authedPrincipal("developer"))
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("forbidden error message = %q, want to contain 'forbidden'", err)
	}
}

// ── AdminRole / SetAdminRole ───────────────────────────────────────────────────

func TestAuthorizer_AdminRole_Default(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	if got := a.AdminRole(); got != "admin" {
		t.Errorf("AdminRole() = %q, want %q", got, "admin")
	}
}

func TestAuthorizer_SetAdminRole(t *testing.T) {
	perms := map[string]Permission{
		"POST /secret": {RequireRoles: []string{"engineer"}, AdminBypass: true},
	}
	a := NewAuthorizer(perms, true)
	a.SetAdminRole("superuser")

	// superuser now bypasses RequireRoles
	if err := a.Authorize("POST /secret", authedPrincipal("superuser")); err != nil {
		t.Errorf("superuser bypass should succeed, got %v", err)
	}

	// original "admin" role no longer bypasses
	err := a.Authorize("POST /secret", authedPrincipal("admin"))
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("'admin' role after SetAdminRole('superuser') = %v, want ErrForbidden", err)
	}
}

// ── RoleGrants ────────────────────────────────────────────────────────────────

func TestAuthorizer_RoleGrants(t *testing.T) {
	a := NewAuthorizer(DefaultGatewayPermissions, true)
	grants := a.RoleGrants()

	// "dba" should only grant POST /api/v1/db/{tool}
	dbaGrants, ok := grants["dba"]
	if !ok {
		t.Fatal("RoleGrants missing 'dba' key")
	}
	if len(dbaGrants) != 1 || dbaGrants[0] != "POST /api/v1/db/{tool}" {
		t.Errorf("dba grants = %v, want [POST /api/v1/db/{tool}]", dbaGrants)
	}

	// "fleet-operator" should only grant POST /api/v1/fleet/jobs
	foGrants, ok := grants["fleet-operator"]
	if !ok {
		t.Fatal("RoleGrants missing 'fleet-operator' key")
	}
	if len(foGrants) != 1 || foGrants[0] != "POST /api/v1/fleet/jobs" {
		t.Errorf("fleet-operator grants = %v, want [POST /api/v1/fleet/jobs]", foGrants)
	}
}

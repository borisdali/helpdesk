// Package authz provides the central HTTP authorization module for the helpdesk
// project. It evaluates a declarative permission table keyed on Go 1.22+ route
// patterns and is applied as middleware to both the gateway and auditd.
//
// Enforcement is opt-in: when enforcing=false (dev / no-auth mode) all checks
// return nil, preserving backward compatibility with deployments that run
// without an identity provider.
package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"helpdesk/internal/identity"
)

// ErrUnauthorized is returned when a request requires authentication but the
// caller is anonymous (no valid credentials provided).
var ErrUnauthorized = errors.New("authentication required")

// ErrForbidden is returned when an authenticated caller lacks the required role.
var ErrForbidden = errors.New("forbidden")

// Permission describes the authorization requirements for a single HTTP route.
// The zero value allows any anonymous caller — prefer AllowAnonymous: true to
// make intent explicit.
type Permission struct {
	// AllowAnonymous, when true, lets any caller through regardless of identity.
	// Use for health checks and public discovery endpoints.
	AllowAnonymous bool

	// RequireRoles lists the roles the caller must have (at least one matches).
	// nil or empty means "any authenticated (non-anonymous) user".
	// Ignored when AllowAnonymous is true.
	RequireRoles []string

	// AdminBypass, when true, lets principals with the "admin" role through
	// regardless of RequireRoles or ServiceOnly. Set to true on every entry in
	// the permission tables.
	AdminBypass bool

	// ServiceOnly, when true, restricts the endpoint to service accounts
	// (principal.Service != ""). Human users are rejected even if they hold the
	// required roles. AdminBypass still applies.
	ServiceOnly bool
}

// Authorizer evaluates permissions for incoming HTTP requests against a static
// permission table. It is safe for concurrent use after construction.
type Authorizer struct {
	permissions map[string]Permission
	enforcing   bool
	adminRole   string
}

// NewAuthorizer constructs an Authorizer from the given permission table.
// Pass enforcing=true when a real identity provider (static or JWT) is
// configured; enforcing=false when running with NoAuthProvider (dev/local mode)
// so that all checks are skipped and existing behavior is preserved.
func NewAuthorizer(permissions map[string]Permission, enforcing bool) *Authorizer {
	return &Authorizer{permissions: permissions, enforcing: enforcing, adminRole: "admin"}
}

// SetAdminRole sets the role name that confers admin bypass privileges.
func (a *Authorizer) SetAdminRole(role string) { a.adminRole = role }

// AdminRole returns the current admin role name.
func (a *Authorizer) AdminRole() string { return a.adminRole }

// RoleGrants inverts the permission table: for each route pattern that has
// RequireRoles set, each role is mapped to the list of patterns it grants
// access to. Routes with only AdminBypass and no RequireRoles are not listed.
// Each role's pattern slice is sorted for stable output. Never returns nil.
func (a *Authorizer) RoleGrants() map[string][]string {
	result := make(map[string][]string)
	for pattern, perm := range a.permissions {
		for _, role := range perm.RequireRoles {
			result[role] = append(result[role], pattern)
		}
	}
	for role := range result {
		sort.Strings(result[role])
	}
	return result
}

// IsEnforcing reports whether authorization checks are active.
func (a *Authorizer) IsEnforcing() bool { return a.enforcing }

// Authorize checks whether principal is permitted to access the route
// identified by pattern (a Go 1.22 ServeMux pattern such as
// "POST /api/v1/db/{tool}").
//
// Returns nil when access is granted.
// Returns ErrUnauthorized when the caller is anonymous but authentication is
// required.
// Returns ErrForbidden when the caller is authenticated but lacks the required
// role or is a human caller on a service-only endpoint.
// Returns nil when the Authorizer is not in enforcing mode.
func (a *Authorizer) Authorize(pattern string, principal identity.ResolvedPrincipal) error {
	if !a.enforcing {
		return nil
	}

	perm, ok := a.permissions[pattern]
	if !ok {
		// Unknown route: fail closed.
		// Anonymous callers get 401; authenticated callers pass through (the mux
		// will return 404 or 405 for them).
		if principal.IsAnonymous() {
			return fmt.Errorf("%w: unknown route", ErrUnauthorized)
		}
		return nil
	}

	if perm.AllowAnonymous {
		return nil
	}

	if principal.IsAnonymous() {
		return fmt.Errorf("%w: this endpoint requires authentication", ErrUnauthorized)
	}

	// Admin bypass (checked before ServiceOnly so admins can always operate).
	if perm.AdminBypass && principal.HasRole(a.adminRole) {
		return nil
	}

	// ServiceOnly: reject human callers.
	if perm.ServiceOnly && principal.Service == "" {
		return fmt.Errorf("%w: endpoint restricted to service accounts", ErrForbidden)
	}

	// Role check: any one of RequireRoles is sufficient.
	if len(perm.RequireRoles) == 0 {
		return nil // any authenticated user
	}
	for _, role := range perm.RequireRoles {
		if principal.HasRole(role) {
			return nil
		}
	}
	return fmt.Errorf("%w: one of roles %v required", ErrForbidden, perm.RequireRoles)
}

// Require checks whether principal holds at least one of the given roles.
// It applies admin bypass automatically. This is a convenience for handlers
// that need dynamic, resource-level checks (e.g., the approve/deny handler
// determines the required role after fetching the approval record).
//
// Returns nil when access is granted, ErrForbidden otherwise.
// Returns nil when the Authorizer is not in enforcing mode.
func (a *Authorizer) Require(principal identity.ResolvedPrincipal, roles ...string) error {
	if !a.enforcing {
		return nil
	}
	if principal.HasRole(a.adminRole) {
		return nil
	}
	for _, role := range roles {
		if principal.HasRole(role) {
			return nil
		}
	}
	if len(roles) == 0 {
		return fmt.Errorf("%w: no roles specified", ErrForbidden)
	}
	return fmt.Errorf("%w: one of roles %v required", ErrForbidden, roles)
}

// contextKey is an unexported type to prevent context key collisions.
type contextKey struct{}

// WithPrincipal stores the resolved principal in ctx. Called by Middleware.
func WithPrincipal(ctx context.Context, p identity.ResolvedPrincipal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// PrincipalFromContext retrieves the principal stored by Middleware.
// Returns a zero ResolvedPrincipal (anonymous) when none is present.
// Handlers call this instead of re-resolving identity from the HTTP request.
func PrincipalFromContext(ctx context.Context) identity.ResolvedPrincipal {
	p, _ := ctx.Value(contextKey{}).(identity.ResolvedPrincipal)
	return p
}

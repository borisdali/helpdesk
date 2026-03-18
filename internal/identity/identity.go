// Package identity resolves verified principals from incoming HTTP requests.
// It is called by the gateway on every request and the result is propagated
// through the entire call chain via A2A message metadata.
package identity

import "net/http"

// Provider authenticates an incoming HTTP request and returns the resolved principal.
// It is called by the gateway once per request, before any agent processing begins.
type Provider interface {
	// Resolve extracts and verifies identity from the HTTP request.
	// Returns an error only when authentication is required and fails
	// (wrong API key, invalid/expired JWT, etc.).
	// In "none" mode Resolve never returns an error.
	Resolve(r *http.Request) (ResolvedPrincipal, error)
}

// ResolvedPrincipal is the verified identity attached to a request.
// It is created by the gateway's identity provider and propagated through
// every downstream call via A2A message metadata fields.
type ResolvedPrincipal struct {
	// UserID is the verified user identifier: email, JWT sub, or service account name.
	UserID string `json:"user_id,omitempty"`

	// Roles is the resolved list of roles for this principal.
	// Empty when identity provider is "none" (no role resolution).
	Roles []string `json:"roles,omitempty"`

	// Service is non-empty when this is a service account (e.g., "srebot", "secbot").
	// Mutually exclusive with UserID for human principals.
	Service string `json:"service,omitempty"`

	// AuthMethod records how identity was established.
	// One of: "api_key", "jwt", "header" (legacy no-auth).
	AuthMethod string `json:"auth_method,omitempty"`
}

// IsAnonymous returns true when identity was not verified
// (AuthMethod == "header", meaning the X-User header was accepted without validation).
func (p ResolvedPrincipal) IsAnonymous() bool {
	return p.AuthMethod == "header"
}

// HasRole returns true if the principal has the given role.
func (p ResolvedPrincipal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// EffectiveID returns the best available identifier for logging and audit:
// Service if set, otherwise UserID.
func (p ResolvedPrincipal) EffectiveID() string {
	if p.Service != "" {
		return p.Service
	}
	return p.UserID
}

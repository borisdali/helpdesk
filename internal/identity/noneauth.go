package identity

import "net/http"

// NoAuthProvider accepts the X-User header as-is without any validation.
// This preserves the existing behavior and is the default when
// HELPDESK_IDENTITY_PROVIDER is unset or "none".
//
// All resolved principals have AuthMethod="header" and no roles.
type NoAuthProvider struct{}

// Resolve reads the X-User header and returns it as an unverified principal.
// Never returns an error.
func (p *NoAuthProvider) Resolve(r *http.Request) (ResolvedPrincipal, error) {
	userID := r.Header.Get("X-User")
	return ResolvedPrincipal{
		UserID:     userID,
		AuthMethod: "header",
	}, nil
}

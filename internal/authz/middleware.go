package authz

import (
	"errors"
	"log/slog"
	"net/http"

	"helpdesk/internal/identity"
)

// Middleware returns an http.Handler middleware that enforces authorization on
// every request. It:
//  1. Resolves the caller's identity using provider.
//  2. Looks up r.Pattern in the permission table and calls Authorize().
//  3. On failure writes 401 (unauthenticated) or 403 (forbidden) and stops.
//  4. On success stores the principal in the request context via WithPrincipal
//     and calls next.
//
// Handlers downstream can retrieve the principal with PrincipalFromContext
// instead of re-resolving identity from the request.
//
// IMPORTANT: r.Pattern is only set by http.ServeMux when dispatching to a
// matched handler, so Middleware must wrap the inner handler (after mux
// routing), not the mux itself. In production, per-pattern auth closures
// inside RegisterRoutes / main.go are preferred. Middleware is retained for
// test helpers that wrap individual handlers directly.
func (a *Authorizer) Middleware(provider identity.Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: resolve identity. A hard error means bad credentials (wrong
			// API key, invalid JWT) — never occurs with NoAuthProvider.
			// On error: fall through as anonymous and let Authorize decide.
			// AllowAnonymous routes pass; protected routes get 401 from Authorize.
			principal, err := provider.Resolve(r)
			if err != nil {
				slog.Debug("authz: unrecognized credential, treating as anonymous",
					"pattern", r.Pattern, "err", err)
				principal = identity.ResolvedPrincipal{AuthMethod: "header"}
			}

			// Step 2: authorize against the route pattern.
			// r.Pattern is populated by Go 1.22+ ServeMux after route matching.
			if authErr := a.Authorize(r.Pattern, principal); authErr != nil {
				status := http.StatusForbidden
				if errors.Is(authErr, ErrUnauthorized) {
					status = http.StatusUnauthorized
				}
				slog.Info("authz: request denied",
					"pattern", r.Pattern,
					"principal", principal.EffectiveID(),
					"anonymous", principal.IsAnonymous(),
					"err", authErr)
				http.Error(w, authErr.Error(), status)
				return
			}

			// Step 3: propagate principal and proceed.
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

package identity

import (
	"fmt"
	"os"
	"time"
)

// NewFromEnv creates the appropriate identity provider based on HELPDESK_IDENTITY_PROVIDER.
// Supported values: "none" (default), "static", "jwt".
func NewFromEnv() (Provider, error) {
	mode := os.Getenv("HELPDESK_IDENTITY_PROVIDER")
	switch mode {
	case "", "none":
		return &NoAuthProvider{}, nil
	case "static":
		path := os.Getenv("HELPDESK_USERS_FILE")
		if path == "" {
			path = "/etc/helpdesk/users.yaml"
		}
		return NewStaticProvider(path)
	case "jwt":
		jwksURL := os.Getenv("HELPDESK_JWT_JWKS_URL")
		if jwksURL == "" {
			return nil, fmt.Errorf("identity: HELPDESK_JWT_JWKS_URL is required for jwt mode")
		}
		cacheTTL := 5 * time.Minute
		if s := os.Getenv("HELPDESK_JWT_CACHE_TTL"); s != "" {
			if d, err := time.ParseDuration(s); err == nil {
				cacheTTL = d
			}
		}
		return NewJWTProvider(JWTConfig{
			JWKSUrl:    jwksURL,
			Issuer:     os.Getenv("HELPDESK_JWT_ISSUER"),
			Audience:   os.Getenv("HELPDESK_JWT_AUDIENCE"),
			RolesClaim: os.Getenv("HELPDESK_JWT_ROLES_CLAIM"),
			CacheTTL:   cacheTTL,
		}), nil
	default:
		return nil, fmt.Errorf("identity: unknown provider mode %q (valid: none, static, jwt)", mode)
	}
}

// PurposeFromRequest extracts the purpose from request headers and body fields.
// Headers take precedence over body-parsed values (passed as purposeFromBody).
// Returns ("", false) when neither source provides a purpose — callers should
// treat an empty purpose as undeclared rather than substituting a default.
func PurposeFromRequest(headerPurpose, bodyPurpose string) (string, bool) {
	if headerPurpose != "" {
		return headerPurpose, true
	}
	if bodyPurpose != "" {
		return bodyPurpose, true
	}
	return "", false
}

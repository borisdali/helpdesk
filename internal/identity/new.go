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
// Falls back to the mode-derived default when neither is set.
// Returns the purpose and a bool indicating whether the purpose was explicitly
// declared by the caller (true) or derived from the operating mode (false).
func PurposeFromRequest(headerPurpose, bodyPurpose, operatingMode string) (string, bool) {
	if headerPurpose != "" {
		return headerPurpose, true
	}
	if bodyPurpose != "" {
		return bodyPurpose, true
	}
	// Derive from operating mode.
	switch operatingMode {
	case "fix":
		return "remediation", false
	default:
		return "diagnostic", false
	}
}

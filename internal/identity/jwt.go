package identity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWTConfig configures the JWT identity provider.
type JWTConfig struct {
	JWKSUrl    string        // e.g., "https://idp.example.com/.well-known/jwks.json"
	Issuer     string        // Expected iss claim value
	Audience   string        // Expected aud claim value (optional)
	RolesClaim string        // JWT claim containing role list (default: "groups")
	CacheTTL   time.Duration // How long to cache JWKS keys (default: 5m)
}

// JWTProvider validates JWTs against a JWKS endpoint and extracts principal identity.
type JWTProvider struct {
	cfg         JWTConfig
	mu          sync.RWMutex
	cachedKeys  map[string]any // kid → *rsa.PublicKey or *ecdsa.PublicKey
	cacheExpiry time.Time
}

// NewJWTProvider creates a new JWT provider with the given config.
func NewJWTProvider(cfg JWTConfig) *JWTProvider {
	if cfg.RolesClaim == "" {
		cfg.RolesClaim = "groups"
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	return &JWTProvider{cfg: cfg}
}

// Resolve validates the JWT Bearer token and extracts the principal.
func (p *JWTProvider) Resolve(r *http.Request) (ResolvedPrincipal, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ResolvedPrincipal{}, fmt.Errorf("identity: Bearer token required for jwt mode")
	}
	token := strings.TrimPrefix(auth, "Bearer ")

	claims, err := p.validateJWT(token)
	if err != nil {
		return ResolvedPrincipal{}, fmt.Errorf("identity: JWT validation failed: %w", err)
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return ResolvedPrincipal{}, fmt.Errorf("identity: JWT missing sub claim")
	}

	if p.cfg.Issuer != "" {
		if iss, _ := claims["iss"].(string); iss != p.cfg.Issuer {
			return ResolvedPrincipal{}, fmt.Errorf("identity: JWT issuer mismatch: got %q want %q", iss, p.cfg.Issuer)
		}
	}

	if p.cfg.Audience != "" {
		if !p.checkAudience(claims) {
			return ResolvedPrincipal{}, fmt.Errorf("identity: JWT audience mismatch")
		}
	}

	roles := p.extractRoles(claims)

	return ResolvedPrincipal{
		UserID:     sub,
		Roles:      roles,
		AuthMethod: "jwt",
	}, nil
}

func (p *JWTProvider) checkAudience(claims map[string]any) bool {
	switch v := claims["aud"].(type) {
	case string:
		return v == p.cfg.Audience
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == p.cfg.Audience {
				return true
			}
		}
	}
	return false
}

func (p *JWTProvider) extractRoles(claims map[string]any) []string {
	raw, ok := claims[p.cfg.RolesClaim]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		roles := make([]string, 0, len(v))
		for _, r := range v {
			if s, ok := r.(string); ok && s != "" {
				roles = append(roles, s)
			}
		}
		return roles
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}

func (p *JWTProvider) validateJWT(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parsing JWT header: %w", err)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("parsing JWT claims: %w", err)
	}

	// Check expiry before fetching keys (fast path for expired tokens).
	if exp, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(exp) {
			return nil, fmt.Errorf("JWT expired")
		}
	}

	key, err := p.getKey(header.Kid)
	if err != nil {
		return nil, err
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT signature: %w", err)
	}

	signingInput := parts[0] + "." + parts[1]
	if err := verifyJWTSignature(header.Alg, signingInput, sig, key); err != nil {
		return nil, err
	}

	return claims, nil
}

func (p *JWTProvider) getKey(kid string) (any, error) {
	p.mu.RLock()
	if time.Now().Before(p.cacheExpiry) {
		if key := p.lookupKey(kid); key != nil {
			p.mu.RUnlock()
			return key, nil
		}
	}
	p.mu.RUnlock()

	keys, err := fetchJWKS(p.cfg.JWKSUrl)
	if err != nil {
		return nil, fmt.Errorf("refreshing JWKS: %w", err)
	}

	p.mu.Lock()
	p.cachedKeys = keys
	p.cacheExpiry = time.Now().Add(p.cfg.CacheTTL)
	p.mu.Unlock()

	p.mu.RLock()
	defer p.mu.RUnlock()
	if key := p.lookupKey(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("key ID %q not found in JWKS (have %d keys)", kid, len(p.cachedKeys))
}

// lookupKey finds a key by kid. When the JWT carries no kid (kid=="") and the
// JWKS contains exactly one key, that key is returned — many dev/internal IdPs
// omit the kid field. Caller must hold at least p.mu.RLock.
func (p *JWTProvider) lookupKey(kid string) any {
	if kid != "" {
		return p.cachedKeys[kid] // may be nil
	}
	// No kid in JWT: use the sole JWKS key if unambiguous.
	if len(p.cachedKeys) == 1 {
		for _, v := range p.cachedKeys {
			return v
		}
	}
	return nil
}

type jwkJSON struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	// RSA fields
	N string `json:"n"`
	E string `json:"e"`
	// EC fields
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func fetchJWKS(jwksURL string) (map[string]any, error) {
	resp, err := http.Get(jwksURL) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("fetching JWKS from %q: %w", jwksURL, err)
	}
	defer resp.Body.Close()

	var body struct {
		Keys []jwkJSON `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("parsing JWKS response: %w", err)
	}

	keys := make(map[string]any, len(body.Keys))
	for _, k := range body.Keys {
		switch k.Kty {
		case "RSA":
			if pub, err := parseRSAPublicKey(k); err == nil {
				keys[k.Kid] = pub
			}
		case "EC":
			if pub, err := parseECPublicKey(k); err == nil {
				keys[k.Kid] = pub
			}
		}
	}
	return keys, nil
}

func parseRSAPublicKey(k jwkJSON) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decoding RSA n: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decoding RSA e: %w", err)
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}

func parseECPublicKey(k jwkJSON) (*ecdsa.PublicKey, error) {
	xb, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("decoding EC x: %w", err)
	}
	yb, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("decoding EC y: %w", err)
	}
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
	}
	return &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}, nil
}

func verifyJWTSignature(alg, signingInput string, sig []byte, key any) error {
	switch alg {
	case "RS256", "RS384", "RS512":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("expected RSA key for algorithm %q", alg)
		}
		h, cryptoHash := algHash(alg)
		h.Write([]byte(signingInput))
		return rsa.VerifyPKCS1v15(pub, cryptoHash, h.Sum(nil), sig)
	case "ES256", "ES384", "ES512":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("expected EC key for algorithm %q", alg)
		}
		h, _ := algHash(alg)
		h.Write([]byte(signingInput))
		if !ecdsa.VerifyASN1(pub, h.Sum(nil), sig) {
			return fmt.Errorf("ECDSA signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported JWT algorithm %q", alg)
	}
}

func algHash(alg string) (hash.Hash, crypto.Hash) {
	switch alg {
	case "RS384", "ES384":
		return sha512.New384(), crypto.SHA384
	case "RS512", "ES512":
		return sha512.New(), crypto.SHA512
	default: // RS256, ES256
		return sha256.New(), crypto.SHA256
	}
}

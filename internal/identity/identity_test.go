package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gocrypto "crypto"

	"golang.org/x/crypto/argon2"
)

// ── ResolvedPrincipal helpers ─────────────────────────────────────────────────

func TestResolvedPrincipal_IsAnonymous(t *testing.T) {
	cases := []struct {
		name string
		p    ResolvedPrincipal
		want bool
	}{
		{"header auth is anonymous", ResolvedPrincipal{AuthMethod: "header"}, true},
		{"api_key is not anonymous", ResolvedPrincipal{AuthMethod: "api_key"}, false},
		{"jwt is not anonymous", ResolvedPrincipal{AuthMethod: "jwt"}, false},
		{"static is not anonymous", ResolvedPrincipal{AuthMethod: "static"}, false},
		{"empty is not anonymous", ResolvedPrincipal{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.IsAnonymous(); got != tc.want {
				t.Errorf("IsAnonymous() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolvedPrincipal_HasRole(t *testing.T) {
	p := ResolvedPrincipal{Roles: []string{"dba", "sre"}}
	if !p.HasRole("dba") {
		t.Error("expected HasRole('dba') = true")
	}
	if !p.HasRole("sre") {
		t.Error("expected HasRole('sre') = true")
	}
	if p.HasRole("developer") {
		t.Error("expected HasRole('developer') = false")
	}
	empty := ResolvedPrincipal{}
	if empty.HasRole("anything") {
		t.Error("empty principal should have no roles")
	}
}

func TestResolvedPrincipal_EffectiveID(t *testing.T) {
	cases := []struct {
		name string
		p    ResolvedPrincipal
		want string
	}{
		{"service takes precedence", ResolvedPrincipal{UserID: "alice", Service: "srebot"}, "srebot"},
		{"user when no service", ResolvedPrincipal{UserID: "alice@example.com"}, "alice@example.com"},
		{"empty when both empty", ResolvedPrincipal{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.EffectiveID(); got != tc.want {
				t.Errorf("EffectiveID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── NoAuthProvider ────────────────────────────────────────────────────────────

func TestNoAuthProvider_WithXUser(t *testing.T) {
	p := &NoAuthProvider{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User", "alice@example.com")

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.UserID != "alice@example.com" {
		t.Errorf("UserID = %q, want 'alice@example.com'", principal.UserID)
	}
	if principal.AuthMethod != "header" {
		t.Errorf("AuthMethod = %q, want 'header'", principal.AuthMethod)
	}
	if len(principal.Roles) != 0 {
		t.Errorf("expected no roles, got %v", principal.Roles)
	}
	if !principal.IsAnonymous() {
		t.Error("NoAuthProvider should produce anonymous principals")
	}
}

func TestNoAuthProvider_NoHeader(t *testing.T) {
	p := &NoAuthProvider{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("NoAuthProvider should never error, got: %v", err)
	}
	if principal.UserID != "" {
		t.Errorf("expected empty UserID, got %q", principal.UserID)
	}
	if principal.AuthMethod != "header" {
		t.Errorf("AuthMethod = %q, want 'header'", principal.AuthMethod)
	}
}

// ── StaticProvider ────────────────────────────────────────────────────────────

// makeArgon2idHash produces an Argon2id hash using low parameters suitable for tests.
func makeArgon2idHash(t *testing.T, key string) string {
	t.Helper()
	salt := []byte("testsalt12345678") // fixed salt for determinism
	// Very low parameters — fast enough for unit tests, not for production.
	hash := argon2.IDKey([]byte(key), salt, 1, 8*1024, 1, 32)
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)
	return fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s", saltB64, hashB64)
}

func writeTempUsersYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "users-*.yaml")
	if err != nil {
		t.Fatalf("create temp users file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp users file: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestStaticProvider_HumanUser(t *testing.T) {
	yaml := `
users:
  - id: alice@example.com
    roles: [dba, sre]
  - id: bob@example.com
    roles: [developer]
`
	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User", "alice@example.com")

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.UserID != "alice@example.com" {
		t.Errorf("UserID = %q, want 'alice@example.com'", principal.UserID)
	}
	if principal.AuthMethod != "static" {
		t.Errorf("AuthMethod = %q, want 'static'", principal.AuthMethod)
	}
	if !principal.HasRole("dba") {
		t.Error("expected role 'dba'")
	}
	if !principal.HasRole("sre") {
		t.Error("expected role 'sre'")
	}
}

func TestStaticProvider_UnknownUser(t *testing.T) {
	yaml := `
users:
  - id: alice@example.com
    roles: [dba]
`
	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User", "unknown@example.com")

	_, err = p.Resolve(r)
	if err == nil {
		t.Fatal("expected error for unknown user, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestStaticProvider_NoCredentials(t *testing.T) {
	yaml := `
users:
  - id: alice@example.com
    roles: [dba]
`
	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	// No credentials at all → anonymous principal, no error.
	// The authorizer decides whether the route permits anonymous access.
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no X-User, no Bearer
	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("expected no error for missing credentials, got: %v", err)
	}
	if !principal.IsAnonymous() {
		t.Errorf("expected anonymous principal, got AuthMethod=%q UserID=%q", principal.AuthMethod, principal.UserID)
	}
}

func TestStaticProvider_ServiceAccount_ValidKey(t *testing.T) {
	apiKey := "super-secret-key-for-tests"
	hash := makeArgon2idHash(t, apiKey)

	yaml := fmt.Sprintf(`
service_accounts:
  - id: srebot
    roles: [sre-automation]
    api_key_hash: "%s"
`, hash)

	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+apiKey)

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Service != "srebot" {
		t.Errorf("Service = %q, want 'srebot'", principal.Service)
	}
	if principal.AuthMethod != "api_key" {
		t.Errorf("AuthMethod = %q, want 'api_key'", principal.AuthMethod)
	}
	if !principal.HasRole("sre-automation") {
		t.Error("expected role 'sre-automation'")
	}
	if principal.UserID != "" {
		t.Errorf("service account should have empty UserID, got %q", principal.UserID)
	}
}

func TestStaticProvider_ServiceAccount_InvalidKey(t *testing.T) {
	apiKey := "super-secret-key-for-tests"
	hash := makeArgon2idHash(t, apiKey)

	yaml := fmt.Sprintf(`
service_accounts:
  - id: srebot
    roles: [sre-automation]
    api_key_hash: "%s"
`, hash)

	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer wrong-key")

	_, err = p.Resolve(r)
	if err == nil {
		t.Fatal("expected error for invalid API key")
	}
	// Error must not reveal which account failed.
	if strings.Contains(err.Error(), "srebot") {
		t.Errorf("error should not reveal account name: %v", err)
	}
}

func TestStaticProvider_ServiceAccount_BearerPrecedence(t *testing.T) {
	// When both X-User and Bearer are present, Bearer (service account) takes precedence.
	apiKey := "my-service-key"
	hash := makeArgon2idHash(t, apiKey)

	yaml := fmt.Sprintf(`
users:
  - id: alice@example.com
    roles: [dba]
service_accounts:
  - id: pipeline
    roles: [ci]
    api_key_hash: "%s"
`, hash)

	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User", "alice@example.com")
	r.Header.Set("Authorization", "Bearer "+apiKey)

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Service != "pipeline" {
		t.Errorf("expected service 'pipeline', got %q", principal.Service)
	}
}

func TestStaticProvider_FileNotFound(t *testing.T) {
	_, err := NewStaticProvider("/nonexistent/path/users.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestStaticProvider_InvalidYAML(t *testing.T) {
	path := writeTempUsersYAML(t, "not: valid: yaml: [[[")
	_, err := NewStaticProvider(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// ── verifyArgon2id ────────────────────────────────────────────────────────────

func TestVerifyArgon2id(t *testing.T) {
	key := "test-key"
	salt := []byte("saltsaltsaltsalt")
	hash := argon2.IDKey([]byte(key), salt, 1, 8*1024, 1, 32)
	hashStr := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))

	ok, err := verifyArgon2id(key, hashStr)
	if err != nil {
		t.Fatalf("verifyArgon2id error: %v", err)
	}
	if !ok {
		t.Error("expected valid key to verify successfully")
	}

	ok, err = verifyArgon2id("wrong-key", hashStr)
	if err != nil {
		t.Fatalf("verifyArgon2id error on wrong key: %v", err)
	}
	if ok {
		t.Error("expected wrong key to fail verification")
	}
}

func TestVerifyArgon2id_InvalidFormat(t *testing.T) {
	cases := []struct{ name, hash string }{
		{"empty", ""},
		{"wrong prefix", "$bcrypt$..."},
		{"too few parts", "$argon2id$v=19$m=65536"},
		{"bad base64 salt", "$argon2id$v=19$m=65536,t=1,p=1$!!!$aGVsbG8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifyArgon2id("key", tc.hash)
			if err == nil {
				t.Error("expected error for invalid hash format")
			}
		})
	}
}

// ── JWTProvider ───────────────────────────────────────────────────────────────

// jwtSignRS256 creates a signed RS256 JWT string.
func jwtSignRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)

	h := sha256.New()
	h.Write([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, gocrypto.SHA256, h.Sum(nil))
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwtSignES256 creates a signed ES256 JWT string.
func jwtSignES256(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)

	h := sha256.New()
	h.Write([]byte(signingInput))
	sig, err := ecdsa.SignASN1(rand.Reader, key, h.Sum(nil))
	if err != nil {
		t.Fatalf("sign ES256 JWT: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// rsaJWKS returns a minimal JWKS JSON for the given RSA public key.
func rsaJWKS(kid string, pub *rsa.PublicKey) []byte {
	nB64 := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "kid": kid,
			"n": nB64, "e": eB64,
		}},
	})
	return body
}

// ecJWKS returns a minimal JWKS JSON for the given EC public key.
func ecJWKS(kid string, pub *ecdsa.PublicKey) []byte {
	xB64 := base64.RawURLEncoding.EncodeToString(pub.X.Bytes())
	yB64 := base64.RawURLEncoding.EncodeToString(pub.Y.Bytes())
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]any{{
			"kty": "EC", "alg": "ES256", "kid": kid, "crv": "P-256",
			"x": xB64, "y": yB64,
		}},
	})
	return body
}

func TestJWTProvider_RS256_ValidToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	kid := "test-key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{
		JWKSUrl:    jwksServer.URL,
		Issuer:     "https://idp.example.com/",
		RolesClaim: "groups",
		CacheTTL:   time.Minute,
	})

	claims := map[string]any{
		"sub":    "alice@example.com",
		"iss":    "https://idp.example.com/",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
		"groups": []string{"dba", "sre"},
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	principal, err := provider.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.UserID != "alice@example.com" {
		t.Errorf("UserID = %q, want 'alice@example.com'", principal.UserID)
	}
	if principal.AuthMethod != "jwt" {
		t.Errorf("AuthMethod = %q, want 'jwt'", principal.AuthMethod)
	}
	if !principal.HasRole("dba") {
		t.Error("expected role 'dba'")
	}
	if !principal.HasRole("sre") {
		t.Error("expected role 'sre'")
	}
}

func TestJWTProvider_ES256_ValidToken(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	kid := "ec-key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(ecJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{
		JWKSUrl:    jwksServer.URL,
		RolesClaim: "roles",
	})

	claims := map[string]any{
		"sub":   "bob@example.com",
		"exp":   float64(time.Now().Add(time.Hour).Unix()),
		"roles": []string{"developer"},
	}
	token := jwtSignES256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	principal, err := provider.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.UserID != "bob@example.com" {
		t.Errorf("UserID = %q, want 'bob@example.com'", principal.UserID)
	}
	if !principal.HasRole("developer") {
		t.Error("expected role 'developer'")
	}
}

func TestJWTProvider_ExpiredToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL})
	claims := map[string]any{
		"sub": "alice",
		"exp": float64(time.Now().Add(-time.Hour).Unix()), // expired
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention 'expired', got: %v", err)
	}
}

func TestJWTProvider_IssuerMismatch(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{
		JWKSUrl: jwksServer.URL,
		Issuer:  "https://expected.issuer.com/",
	})
	claims := map[string]any{
		"sub": "alice",
		"iss": "https://different.issuer.com/",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error for issuer mismatch")
	}
	if !strings.Contains(err.Error(), "issuer") {
		t.Errorf("error should mention 'issuer', got: %v", err)
	}
}

func TestJWTProvider_AudienceMismatch(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{
		JWKSUrl:  jwksServer.URL,
		Audience: "helpdesk",
	})
	claims := map[string]any{
		"sub": "alice",
		"aud": "other-service",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error for audience mismatch")
	}
	if !strings.Contains(err.Error(), "audience") {
		t.Errorf("error should mention 'audience', got: %v", err)
	}
}

func TestJWTProvider_AudienceArray(t *testing.T) {
	// JWT aud may be a string array; provider should accept if target is present.
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{
		JWKSUrl:  jwksServer.URL,
		Audience: "helpdesk",
	})
	claims := map[string]any{
		"sub": "alice",
		"aud": []string{"other-service", "helpdesk"},
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	principal, err := provider.Resolve(r)
	if err != nil {
		t.Fatalf("expected success with aud array containing target: %v", err)
	}
	if principal.UserID != "alice" {
		t.Errorf("UserID = %q, want 'alice'", principal.UserID)
	}
}

func TestJWTProvider_WrongSignature(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "key-1"

	// JWKS serves key, but token is signed with otherKey.
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL})
	claims := map[string]any{
		"sub": "alice",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwtSignRS256(t, otherKey, kid, claims) // wrong key

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error for wrong signature")
	}
}

func TestJWTProvider_MissingSubClaim(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL})
	claims := map[string]any{
		// no "sub" claim
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error for missing sub")
	}
	if !strings.Contains(err.Error(), "sub") {
		t.Errorf("error should mention 'sub', got: %v", err)
	}
}

func TestJWTProvider_NoBearerToken(t *testing.T) {
	provider := NewJWTProvider(JWTConfig{JWKSUrl: "http://unused"})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Authorization header.

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error when no Bearer token")
	}
}

func TestJWTProvider_MalformedToken(t *testing.T) {
	provider := NewJWTProvider(JWTConfig{JWKSUrl: "http://unused"})

	cases := []struct{ name, token string }{
		{"empty", ""},
		{"one part", "abc"},
		{"two parts", "abc.def"},
		{"bad base64 header", "!!!.eyJzdWIiOiJ0ZXN0In0.sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			_, err := provider.Resolve(r)
			if err == nil {
				t.Error("expected error for malformed token")
			}
		})
	}
}

func TestJWTProvider_JWKSUnreachable(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "k1"

	// Build a syntactically valid token pointing at a port nothing is listening on.
	claims := map[string]any{"sub": "alice", "exp": float64(time.Now().Add(time.Hour).Unix())}
	provider := NewJWTProvider(JWTConfig{JWKSUrl: "http://127.0.0.1:1"})
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error when JWKS unreachable")
	}
}

func TestJWTProvider_RolesClaimDefault(t *testing.T) {
	// Default roles claim is "groups".
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "k1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	// RolesClaim not set — should default to "groups".
	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL})

	claims := map[string]any{
		"sub":    "carol",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
		"groups": []string{"sre"},
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	principal, err := provider.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !principal.HasRole("sre") {
		t.Errorf("expected role 'sre' from default 'groups' claim, got roles: %v", principal.Roles)
	}
}

func TestJWTProvider_NoRolesClaim(t *testing.T) {
	// A valid token with no roles claim should resolve with empty roles (not an error).
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "k1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL, RolesClaim: "groups"})
	claims := map[string]any{
		"sub": "dave",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		// no "groups" claim
	}
	token := jwtSignRS256(t, key, kid, claims)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	principal, err := provider.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(principal.Roles) != 0 {
		t.Errorf("expected empty roles, got %v", principal.Roles)
	}
}

func TestJWTProvider_JWKS_KeyCaching(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	kid := "k1"
	fetchCount := 0

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		w.Write(rsaJWKS(kid, &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{
		JWKSUrl:  jwksServer.URL,
		CacheTTL: time.Minute, // long cache — all 3 requests should hit the cache
	})

	for i := 0; i < 3; i++ {
		claims := map[string]any{"sub": "alice", "exp": float64(time.Now().Add(time.Hour).Unix())}
		token := jwtSignRS256(t, key, kid, claims)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if _, err := provider.Resolve(r); err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}

	if fetchCount != 1 {
		t.Errorf("JWKS should be fetched exactly once due to caching, got %d fetches", fetchCount)
	}
}

// ── lookupKey: no-kid fallback ────────────────────────────────────────────────

// TestJWTProvider_NoKid_SingleKey verifies that a JWT with no kid field is
// accepted when the JWKS contains exactly one key (common in dev/internal IdPs).
func TestJWTProvider_NoKid_SingleKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)

	// JWKS has one key with a kid; JWT header omits the kid entirely.
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rsaJWKS("only-key", &key.PublicKey))
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL})

	// Sign a JWT without a kid in the header.
	claims := map[string]any{"sub": "alice@example.com", "exp": float64(time.Now().Add(time.Hour).Unix())}
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"}) // no kid
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	h := sha256.New()
	h.Write([]byte(signingInput))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, gocrypto.SHA256, h.Sum(nil))
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	principal, err := provider.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.UserID != "alice@example.com" {
		t.Errorf("UserID = %q, want alice@example.com", principal.UserID)
	}
}

// TestJWTProvider_NoKid_MultipleKeys verifies that a JWT with no kid is rejected
// when the JWKS contains multiple keys (ambiguous — cannot pick the right one).
func TestJWTProvider_NoKid_MultipleKeys(t *testing.T) {
	key1, _ := rsa.GenerateKey(rand.Reader, 2048)
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n1 := base64.RawURLEncoding.EncodeToString(key1.PublicKey.N.Bytes())
		e1 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key1.PublicKey.E)).Bytes())
		n2 := base64.RawURLEncoding.EncodeToString(key2.PublicKey.N.Bytes())
		e2 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key2.PublicKey.E)).Bytes())
		body, _ := json.Marshal(map[string]any{"keys": []map[string]any{
			{"kty": "RSA", "alg": "RS256", "kid": "key-a", "n": n1, "e": e1},
			{"kty": "RSA", "alg": "RS256", "kid": "key-b", "n": n2, "e": e2},
		}})
		w.Write(body)
	}))
	defer jwksServer.Close()

	provider := NewJWTProvider(JWTConfig{JWKSUrl: jwksServer.URL})

	claims := map[string]any{"sub": "alice", "exp": float64(time.Now().Add(time.Hour).Unix())}
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"}) // no kid
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	h := sha256.New()
	h.Write([]byte(signingInput))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key1, gocrypto.SHA256, h.Sum(nil))
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Resolve(r)
	if err == nil {
		t.Fatal("expected error for no-kid JWT with multiple JWKS keys, got nil")
	}
	if !strings.Contains(err.Error(), "not found in JWKS") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── HashAPIKey / verifyArgon2id round-trip ────────────────────────────────────

func TestHashAPIKey_RoundTrip(t *testing.T) {
	key := "my-test-secret-key"
	hash, err := HashAPIKey(key)
	if err != nil {
		t.Fatalf("HashAPIKey: %v", err)
	}
	ok, err := verifyArgon2id(key, hash)
	if err != nil {
		t.Fatalf("verifyArgon2id: %v", err)
	}
	if !ok {
		t.Error("correct key did not verify against its own hash")
	}
}

func TestHashAPIKey_WrongKey(t *testing.T) {
	hash, err := HashAPIKey("correct-key")
	if err != nil {
		t.Fatalf("HashAPIKey: %v", err)
	}
	ok, err := verifyArgon2id("wrong-key", hash)
	if err != nil {
		t.Fatalf("verifyArgon2id: %v", err)
	}
	if ok {
		t.Error("wrong key should not verify")
	}
}

func TestHashAPIKey_UniqueHashes(t *testing.T) {
	// Two hashes of the same key must differ (random salt).
	h1, _ := HashAPIKey("same-key")
	h2, _ := HashAPIKey("same-key")
	if h1 == h2 {
		t.Error("two hashes of the same key should differ due to random salt")
	}
	// But both must verify correctly.
	for _, h := range []string{h1, h2} {
		ok, err := verifyArgon2id("same-key", h)
		if err != nil || !ok {
			t.Errorf("hash %q did not verify: err=%v ok=%v", h, err, ok)
		}
	}
}

// ── Role alias tests ──────────────────────────────────────────────────────────

func TestStaticProvider_RoleAlias_Expanded(t *testing.T) {
	yaml := `
role_aliases:
  db-admin: dba
users:
  - id: alice@example.com
    roles: [db-admin]
`
	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User", "alice@example.com")

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !principal.HasRole("dba") {
		t.Errorf("expected aliased role 'dba', got roles: %v", principal.Roles)
	}
	if principal.HasRole("db-admin") {
		t.Errorf("aliased role 'db-admin' should not appear in resolved roles, got: %v", principal.Roles)
	}
}

func TestStaticProvider_RoleAlias_UnknownRolePassthrough(t *testing.T) {
	yaml := `
role_aliases:
  db-admin: dba
users:
  - id: alice@example.com
    roles: [sre]
`
	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User", "alice@example.com")

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !principal.HasRole("sre") {
		t.Errorf("role 'sre' not in alias map should pass through, got: %v", principal.Roles)
	}
}

func TestStaticProvider_RoleAliases_NoAliases(t *testing.T) {
	yaml := `
users:
  - id: alice@example.com
    roles: [dba]
`
	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	aliases := p.RoleAliases()
	if aliases == nil {
		t.Error("RoleAliases() should never return nil")
	}
	if len(aliases) != 0 {
		t.Errorf("RoleAliases() with no aliases section = %v, want empty map", aliases)
	}
}

func TestStaticProvider_RoleAlias_ServiceAccount(t *testing.T) {
	apiKey := "svc-alias-test-key"
	hash := makeArgon2idHash(t, apiKey)

	yaml := fmt.Sprintf(`
role_aliases:
  sre-bot: sre-automation
service_accounts:
  - id: mybot
    roles: [sre-bot]
    api_key_hash: "%s"
`, hash)

	path := writeTempUsersYAML(t, yaml)
	p, err := NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+apiKey)

	principal, err := p.Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !principal.HasRole("sre-automation") {
		t.Errorf("expected aliased role 'sre-automation', got roles: %v", principal.Roles)
	}
	if principal.HasRole("sre-bot") {
		t.Errorf("aliased role 'sre-bot' should not appear in resolved roles, got: %v", principal.Roles)
	}
}

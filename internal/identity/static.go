package identity

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"gopkg.in/yaml.v3"
)

// StaticProvider resolves identity from a users.yaml config file.
// Human users authenticate via X-User header (cross-referenced against the file).
// Service accounts authenticate via Authorization: Bearer <api-key>.
type StaticProvider struct {
	// users maps user ID → roles
	users map[string][]string
	// serviceAccounts maps service ID → ServiceAccount
	serviceAccounts map[string]ServiceAccount
}

// NewStaticProvider loads the users config from the given YAML file path.
func NewStaticProvider(path string) (*StaticProvider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity: reading users file %q: %w", path, err)
	}

	var cfg UsersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("identity: parsing users file: %w", err)
	}

	p := &StaticProvider{
		users:           make(map[string][]string, len(cfg.Users)),
		serviceAccounts: make(map[string]ServiceAccount, len(cfg.ServiceAccounts)),
	}
	for _, u := range cfg.Users {
		p.users[u.ID] = u.Roles
	}
	for _, sa := range cfg.ServiceAccounts {
		p.serviceAccounts[sa.ID] = sa
	}
	return p, nil
}

// Resolve authenticates the request.
// Service accounts use Authorization: Bearer <api-key>.
// Human users use X-User: <email> (must be listed in users.yaml).
func (p *StaticProvider) Resolve(r *http.Request) (ResolvedPrincipal, error) {
	// Check for service account bearer token first.
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		key := strings.TrimPrefix(auth, "Bearer ")
		return p.resolveServiceAccount(key)
	}

	// Fall through to human user via X-User header.
	userID := r.Header.Get("X-User")
	if userID == "" {
		return ResolvedPrincipal{}, fmt.Errorf("identity: no credentials provided (X-User header or Bearer token required)")
	}

	roles, ok := p.users[userID]
	if !ok {
		return ResolvedPrincipal{}, fmt.Errorf("identity: user %q not found in users config", userID)
	}

	return ResolvedPrincipal{
		UserID:     userID,
		Roles:      roles,
		AuthMethod: "static",
	}, nil
}

func (p *StaticProvider) resolveServiceAccount(apiKey string) (ResolvedPrincipal, error) {
	// Try each service account — we must not leak which ID failed vs key failed.
	// Iterate all and use constant-time comparison.
	for id, sa := range p.serviceAccounts {
		ok, err := verifyArgon2id(apiKey, sa.APIKeyHash)
		if err != nil {
			continue // malformed hash — skip
		}
		if ok {
			return ResolvedPrincipal{
				Service:    id,
				Roles:      sa.Roles,
				AuthMethod: "api_key",
			}, nil
		}
	}
	return ResolvedPrincipal{}, fmt.Errorf("identity: invalid API key")
}

// verifyArgon2id checks a plaintext key against an Argon2id hash string.
// Expected format: $argon2id$v=19$m=<m>,t=<t>,p=<p>$<base64-salt>$<base64-hash>
func verifyArgon2id(key, hashStr string) (bool, error) {
	parts := strings.Split(hashStr, "$")
	// $argon2id$v=19$m=65536,t=1,p=4$salt$hash → ["", "argon2id", "v=19", "params", "salt", "hash"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid hash format")
	}

	// Parse parameters: m=65536,t=1,p=4
	var memory, time_, threads uint32
	for _, kv := range strings.Split(parts[3], ",") {
		pair := strings.SplitN(kv, "=", 2)
		if len(pair) != 2 {
			return false, fmt.Errorf("invalid param %q", kv)
		}
		v, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil {
			return false, fmt.Errorf("invalid param value %q", pair[1])
		}
		switch pair[0] {
		case "m":
			memory = uint32(v)
		case "t":
			time_ = uint32(v)
		case "p":
			threads = uint32(v)
		}
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("invalid salt encoding")
	}
	storedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("invalid hash encoding")
	}

	computed := argon2.IDKey([]byte(key), salt, time_, memory, uint8(threads), uint32(len(storedHash)))
	return subtle.ConstantTimeCompare(computed, storedHash) == 1, nil
}

// jwttest is a development helper that:
//  1. Generates an in-memory RSA-2048 key pair.
//  2. Serves a JWKS endpoint at http://localhost:<port>/jwks.json.
//  3. Prints a signed RS256 JWT you can use with the gateway JWT provider.
//
// Usage:
//
//	go run ./cmd/jwttest                      # defaults: port 9999, sub alice@example.com
//	go run ./cmd/jwttest -sub bob@example.com -groups sre,oncall
//	go run ./cmd/jwttest -port 9998 -iss https://myidp.example.com -aud myapp
//
// Copy the printed TOKEN and use it with:
//
//	curl -H "Authorization: Bearer $TOKEN" ...
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	port := flag.Int("port", 9999, "Port to serve JWKS on")
	addr := flag.String("addr", "127.0.0.1", "Address to bind the JWKS server on (use 0.0.0.0 to accept connections from Docker containers)")
	sub := flag.String("sub", "alice@example.com", "JWT sub claim (user identity)")
	iss := flag.String("iss", "https://idp.example.com", "JWT iss claim")
	aud := flag.String("aud", "helpdesk", "JWT aud claim")
	groups := flag.String("groups", "dba,sre", "Comma-separated groups/roles for the groups claim")
	ttl := flag.Duration("ttl", time.Hour, "Token validity duration")
	kid := flag.String("kid", "dev-key-1", "Key ID for the JWK (empty string to omit kid)")
	flag.Parse()

	// Generate RSA key pair.
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generating RSA key: %v", err)
	}
	pub := &privateKey.PublicKey

	// Build JWKS JSON.
	jwk := map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
	if *kid != "" {
		jwk["kid"] = *kid
	}
	jwksBody, _ := json.Marshal(map[string]any{"keys": []any{jwk}})

	// Generate RS256 JWT.
	now := time.Now()

	var groupList []string
	for _, g := range strings.Split(*groups, ",") {
		if g = strings.TrimSpace(g); g != "" {
			groupList = append(groupList, g)
		}
	}

	headerMap := map[string]string{"alg": "RS256", "typ": "JWT"}
	if *kid != "" {
		headerMap["kid"] = *kid
	}
	headerJSON, _ := json.Marshal(headerMap)
	claimsJSON, _ := json.Marshal(map[string]any{
		"sub":    *sub,
		"iss":    *iss,
		"aud":    *aud,
		"iat":    now.Unix(),
		"exp":    now.Add(*ttl).Unix(),
		"groups": groupList,
	})

	h64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	c64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := h64 + "." + c64

	digest := sha256.Sum256([]byte(signingInput))
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		log.Fatalf("signing JWT: %v", err)
	}
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sigBytes)

	listenAddr := fmt.Sprintf("%s:%d", *addr, *port)
	// For the printed JWKS URL, always use localhost regardless of bind address
	// so the gateway env var example is correct for the host deployment.
	jwksURL := fmt.Sprintf("http://localhost:%d/jwks.json", *port)

	fmt.Fprintf(os.Stderr, "JWKS server:  %s\n", jwksURL)
	fmt.Fprintf(os.Stderr, "Subject:      %s\n", *sub)
	fmt.Fprintf(os.Stderr, "Groups:       %s\n", *groups)
	fmt.Fprintf(os.Stderr, "Expires:      %s\n", now.Add(*ttl).Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Start gateway with:\n")
	fmt.Fprintf(os.Stderr, "  HELPDESK_IDENTITY_PROVIDER=jwt \\\n")
	fmt.Fprintf(os.Stderr, "  HELPDESK_JWT_JWKS_URL=%s \\\n", jwksURL)
	fmt.Fprintf(os.Stderr, "  HELPDESK_JWT_ISSUER=%s \\\n", *iss)
	fmt.Fprintf(os.Stderr, "  HELPDESK_JWT_AUDIENCE=%s \\\n", *aud)
	fmt.Fprintf(os.Stderr, "  HELPDESK_JWT_ROLES_CLAIM=groups \\\n")
	fmt.Fprintf(os.Stderr, "  go run ./cmd/gateway\n")
	fmt.Fprintf(os.Stderr, "\nThen send requests with:\n")
	fmt.Fprintf(os.Stderr, "  TOKEN=%q\n", token)
	fmt.Fprintf(os.Stderr, "  curl -H \"Authorization: Bearer $TOKEN\" ...\n")
	fmt.Fprintf(os.Stderr, "\nServing JWKS (Ctrl-C to stop)...\n")

	// Print the raw token to stdout for easy capture: TOKEN=$(go run ./cmd/jwttest)
	fmt.Println(token)

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksBody)
	})
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

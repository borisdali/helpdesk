//go:build integration

// Package integration contains integration tests that require Docker infrastructure.
//
// Run with: go test -tags integration -timeout 120s ./testing/integration/...
//
// Prerequisites:
//   - Docker running
//   - docker compose -f testing/docker/docker-compose.yaml up -d --wait
package integration

import (
	"testing"
)

func TestPlaceholder(t *testing.T) {
	t.Skip("Integration tests not yet implemented. See plan for Layer 3.")
}

//go:build faulttest

// Package faulttest contains fault injection tests that require Docker + running agents + LLM API key.
//
// Run with: go test -tags faulttest -timeout 600s -v ./testing/faulttest/...
//
// Prerequisites:
//   - Docker running
//   - docker compose -f testing/docker/docker-compose.yaml up -d --wait
//   - Agents running (database-agent, k8s-agent, or orchestrator)
//   - HELPDESK_API_KEY environment variable set
package faulttest

import (
	"testing"
)

func TestPlaceholder(t *testing.T) {
	t.Skip("Fault injection tests not yet implemented. See plan for Layer 4.")
}

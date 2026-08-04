//go:build integration

package governance

import (
	"net/http"
	"testing"
)

// TestIntegration_GatewayQuery_PlaybookAutoSelection verifies the real
// gateway auto-selects and runs an existing triage playbook for a free-text
// query whose message strongly matches the playbook's real symptoms —
// exercising the actual HTTP → gateway → auditd path (real binaries, not
// mocks), not just the unit-level RoutingDecision/matchPlaybookByKeywords
// tests in cmd/gateway.
//
// pbs_k8s_node_pressure_triage is a real, auto-seeded system playbook —
// SeedSystemPlaybooks runs unconditionally on every auditd startup
// (cmd/auditd/main.go), so no manual seeding is needed here.
//
// No LLM is configured for this gateway process (HELPDESK_MODEL_VENDOR is
// unset in TestMain's gwProc.Env), so a passing result here specifically
// proves the keyword fast-path — which deliberately skips the routing LLM
// entirely — works end-to-end against the real HTTP stack.
//
// Distinguishing signal: this gateway process has no agent registered under
// the name "k8s_agent" (TestMain's discovery stub registers only
// "stub-agent", and is closed immediately after startup). So:
//   - if playbook selection succeeds, the request reaches
//     runQueryViaPlaybook -> handlePlaybookRun -> the agent-proxy step,
//     which fails with 502 (agent unavailable) — confirming dispatch
//     reached that stage.
//   - if selection does NOT happen, handleQuery falls through to
//     routeWithLLM, which fails immediately with 503 (LLM routing not
//     configured) since no LLM is wired up for this process.
//
// The companion regression-control test below confirms an unrelated query
// takes the second path, proving these two failure modes are genuinely
// distinguishable rather than coincidentally identical.
func TestIntegration_GatewayQuery_PlaybookAutoSelection(t *testing.T) {
	status, body := postStatus(t, gatewayAddr, "/api/v1/query", map[string]any{
		"message": "node experiencing MemoryPressure condition, Evicted event in namespace events",
	})
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (playbook auto-selected, dispatch reached the unavailable k8s_agent); body: %s", status, body)
	}
}

// TestIntegration_GatewayQuery_UnmatchedQuery_FallsThroughToLLMRouting is the
// regression control for TestIntegration_GatewayQuery_PlaybookAutoSelection:
// a message that matches no playbook's symptoms must fall through to
// ordinary LLM-based agent routing, which fails immediately with 503 since
// no LLM is configured for this test gateway process — a different,
// distinguishable failure mode from the 502 above. Without this control,
// the 502 assertion above wouldn't prove selection actually happened (any
// query might coincidentally 502 for some unrelated reason).
func TestIntegration_GatewayQuery_UnmatchedQuery_FallsThroughToLLMRouting(t *testing.T) {
	status, body := postStatus(t, gatewayAddr, "/api/v1/query", map[string]any{
		"message": "how does VACUUM work in postgres?",
	})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no playbook match, LLM routing not configured); body: %s", status, body)
	}
}

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

// ── buildHistorySection ───────────────────────────────────────────────────────

func TestBuildHistorySection_Empty(t *testing.T) {
	out := buildHistorySection(nil)
	if !strings.Contains(out, "first tool call") {
		t.Errorf("empty history should mention 'first tool call', got: %q", out)
	}
}

func TestBuildHistorySection_WithSteps(t *testing.T) {
	steps := []*audit.PlaybookRunStep{
		{StepIndex: 1, Tool: "get_blocking_queries", Args: map[string]any{"connection_string": "host=localhost"}, Result: "1 blocker found"},
		{StepIndex: 2, Tool: "terminate_connection", Args: map[string]any{"pid": 1234}, Result: "terminated"},
	}
	out := buildHistorySection(steps)

	if !strings.Contains(out, "Tool call #1") {
		t.Error("output missing 'Tool call #1'")
	}
	if !strings.Contains(out, "get_blocking_queries") {
		t.Error("output missing tool name from step 1")
	}
	if !strings.Contains(out, "1 blocker found") {
		t.Error("output missing result from step 1")
	}
	if !strings.Contains(out, "Tool call #2") {
		t.Error("output missing 'Tool call #2'")
	}
	if !strings.Contains(out, "terminate_connection") {
		t.Error("output missing tool name from step 2")
	}
}

// ── extractFirstJSON ──────────────────────────────────────────────────────────

func TestExtractFirstJSON_Clean(t *testing.T) {
	input := `{"action":"execute_step","tool":"terminate_connection"}`
	got := extractFirstJSON(input)
	if got != input {
		t.Errorf("clean JSON should be returned as-is, got: %q", got)
	}
}

func TestExtractFirstJSON_MarkdownFence(t *testing.T) {
	input := "```json\n{\"action\":\"complete\",\"summary\":\"done\"}\n```"
	got := extractFirstJSON(input)
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("markdown-fenced JSON should be extracted, got: %q", got)
	}
	if !strings.Contains(got, "complete") {
		t.Error("extracted JSON should contain 'complete'")
	}
}

func TestExtractFirstJSON_LeadingText(t *testing.T) {
	input := "Sure! Here is the next step:\n{\"action\":\"execute_step\",\"tool\":\"get_blocking_queries\",\"args\":{}}"
	got := extractFirstJSON(input)
	if !strings.HasPrefix(got, "{") {
		t.Errorf("should extract JSON from leading text, got: %q", got)
	}
}

func TestExtractFirstJSON_NoJSON(t *testing.T) {
	input := "I cannot determine the next step."
	got := extractFirstJSON(input)
	// No JSON — returns original string (trimmed), no crash.
	if got != input {
		t.Errorf("no-JSON input should be returned unchanged, got: %q", got)
	}
}

func TestExtractFirstJSON_BraceInSummary(t *testing.T) {
	// Summary containing } must not confuse extraction (was a real bug: LastIndex
	// found the } inside the string, truncating the JSON and leaving trailing text).
	input := "```json\n{\"action\":\"complete\",\"summary\":\"reset bgwriter_lru_maxpages {was 2} to 100\"}\n```"
	got := extractFirstJSON(input)
	var result struct {
		Action  string `json:"action"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("extracted JSON failed to unmarshal: %v (got: %q)", err, got)
	}
	if result.Action != "complete" {
		t.Errorf("expected action=complete, got %q", result.Action)
	}
}

// ── proposeNextStep ───────────────────────────────────────────────────────────

func TestProposeNextStep_NoLLM(t *testing.T) {
	gw := &Gateway{plannerLLM: nil}
	pb := &audit.Playbook{Name: "Test", Guidance: "Step 1: do X"}

	_, _, _, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", nil)
	if err == nil {
		t.Error("expected error when plannerLLM is nil")
	}
}

func TestProposeNextStep_Complete(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return `{"action":"complete","summary":"Root blocker terminated; locks cleared."}`, nil
		},
	}
	pb := &audit.Playbook{Name: "Idle Blocker Remediate", Guidance: "Terminate root blocker."}

	proposal, done, summary, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", nil)
	if err != nil {
		t.Fatalf("proposeNextStep: %v", err)
	}
	if !done {
		t.Error("expected done=true for action=complete")
	}
	if proposal != nil {
		t.Error("proposal should be nil when done")
	}
	if !strings.Contains(summary, "terminated") {
		t.Errorf("summary = %q, want it to mention 'terminated'", summary)
	}
}

func TestProposeNextStep_ExecuteStep(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return `{
				"action": "execute_step",
				"agent": "database",
				"tool": "terminate_connection",
				"args": {"pid": 4567},
				"reason": "Terminate idle-in-transaction session holding locks"
			}`, nil
		},
	}
	pb := &audit.Playbook{Name: "Idle Blocker Remediate", Guidance: "Terminate root blocker."}

	proposal, done, _, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", nil)
	if err != nil {
		t.Fatalf("proposeNextStep: %v", err)
	}
	if done {
		t.Error("expected done=false for action=execute_step")
	}
	if proposal == nil {
		t.Fatal("expected non-nil proposal")
	}
	if proposal.Tool != "terminate_connection" {
		t.Errorf("tool = %q, want terminate_connection", proposal.Tool)
	}
	if proposal.Index != 1 {
		t.Errorf("index = %d, want 1 (empty history)", proposal.Index)
	}
	if proposal.Agent != "database" {
		t.Errorf("agent = %q, want database", proposal.Agent)
	}
}

func TestProposeNextStep_DefaultsAgentToDatabaseWhenEmpty(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return `{"action":"execute_step","tool":"get_blocking_queries","args":{}}`, nil
		},
	}
	pb := &audit.Playbook{Name: "p", Guidance: "g"}

	proposal, _, _, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", nil)
	if err != nil {
		t.Fatalf("proposeNextStep: %v", err)
	}
	if proposal.Agent != "database" {
		t.Errorf("agent = %q, want database (default)", proposal.Agent)
	}
}

// TestProposeNextStep_AgentAlwaysFromPlaybook_NeverFromLLM verifies the fix
// for a real bug found via manual verification: proposeNextStep must always
// route to the playbook's own agent_name, never to a value the LLM echoes
// back in its JSON response. Before the fix, an LLM response carrying a
// stray "agent" field (e.g. copied from the prompt's own JSON example,
// which used to hardcode "agent": "database") would silently misroute a
// k8s-agent playbook's tool call to the database agent's HTTP endpoint,
// producing a confusing "unknown tool" error instead of executing the
// intended k8s tool.
func TestProposeNextStep_AgentAlwaysFromPlaybook_NeverFromLLM(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			// Simulate a model that (incorrectly) echoes an "agent" field —
			// stepProposerResult has no Agent field at all, so this key is
			// simply ignored during JSON decoding; this test proves it can't
			// influence proposal.Agent even if the model attempts it.
			return `{"action":"execute_step","agent":"database","tool":"get_pods","args":{"namespace":"helpdesk-test"}}`, nil
		},
	}
	pb := &audit.Playbook{Name: "K8s Node Pressure Remediation", Guidance: "g", AgentName: "k8s_agent"}

	proposal, _, _, err := gw.proposeNextStep(context.Background(), pb, "", "", "", nil)
	if err != nil {
		t.Fatalf("proposeNextStep: %v", err)
	}
	if proposal.Agent != "k8s" {
		t.Errorf("Agent = %q, want %q (the playbook's own agent_name, not the LLM's echoed value)",
			proposal.Agent, "k8s")
	}
	if proposal.Tool != "get_pods" {
		t.Errorf("Tool = %q, want get_pods", proposal.Tool)
	}
}

func TestProposeNextStep_IndexIncrementsByHistory(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return `{"action":"execute_step","tool":"get_blocking_queries","args":{}}`, nil
		},
	}
	pb := &audit.Playbook{Name: "p", Guidance: "g"}
	history := []*audit.PlaybookRunStep{
		{StepIndex: 1, Tool: "some_previous_tool", Result: "done"},
		{StepIndex: 2, Tool: "another_tool", Result: "done"},
	}

	proposal, _, _, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", history)
	if err != nil {
		t.Fatalf("proposeNextStep: %v", err)
	}
	if proposal.Index != 3 {
		t.Errorf("index = %d, want 3 (len(history)+1)", proposal.Index)
	}
}

func TestProposeNextStep_BadJSON(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return "I cannot determine the next step.", nil
		},
	}
	pb := &audit.Playbook{Name: "p", Guidance: "g"}

	_, _, _, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", nil)
	if err == nil {
		t.Error("expected error for non-JSON LLM response")
	}
}

func TestProposeNextStep_EmptyToolName(t *testing.T) {
	gw := &Gateway{
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return `{"action":"execute_step","tool":"","args":{}}`, nil
		},
	}
	pb := &audit.Playbook{Name: "p", Guidance: "g"}

	_, _, _, err := gw.proposeNextStep(context.Background(), pb, "host=localhost", "", "", nil)
	if err == nil {
		t.Error("expected error for empty tool name")
	}
}

func TestProposeNextStep_HistoryAppearsInPrompt(t *testing.T) {
	var capturedPrompt string
	gw := &Gateway{
		plannerLLM: func(_ context.Context, prompt string) (string, error) {
			capturedPrompt = prompt
			return `{"action":"complete","summary":"done"}`, nil
		},
	}
	pb := &audit.Playbook{Name: "Test PB", Guidance: "Do the thing."}
	history := []*audit.PlaybookRunStep{
		{StepIndex: 1, Tool: "get_blocking_queries", Args: map[string]any{}, Result: "blocker found pid=9999"},
	}

	gw.proposeNextStep(context.Background(), pb, "host=prod", "", "", history) //nolint:errcheck

	if !strings.Contains(capturedPrompt, "blocker found pid=9999") {
		t.Error("history result should appear in the LLM prompt")
	}
	if !strings.Contains(capturedPrompt, "host=prod") {
		t.Error("connection string should appear in the LLM prompt")
	}
	if !strings.Contains(capturedPrompt, "Do the thing") {
		t.Error("guidance should appear in the LLM prompt")
	}
}

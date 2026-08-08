package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveFakeEvents returns an httptest.Server that responds to GET /v1/events with
// the subset of the given events matching the request's event_type query param —
// mirroring the real auditd server's filtering, since buildDelegationVerification
// now issues separate fetches for tool_execution, agent_reasoning, and
// policy_decision events and each fetch must only see events of its own type.
func serveFakeEvents(t *testing.T, events []Event) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/events") {
			http.NotFound(w, r)
			return
		}
		wantType := EventType(r.URL.Query().Get("event_type"))
		var filtered []Event
		for _, ev := range events {
			if wantType == "" || ev.EventType == wantType {
				filtered = append(filtered, ev)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(filtered) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildDelegationVerification_Mismatch(t *testing.T) {
	// Audit trail contains only read tools — no destructive tool executed.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "get_session_info"}},
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "check_connection"}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionDestructive, "evt_del1", "postgres_database_agent")

	if !v.Mismatch {
		t.Error("Mismatch = false, want true: destructive delegation with no destructive tool confirmed")
	}
	if len(v.DestructiveConfirmed) != 0 {
		t.Errorf("DestructiveConfirmed = %v, want empty", v.DestructiveConfirmed)
	}
	if len(v.ToolsConfirmed) != 2 {
		t.Errorf("ToolsConfirmed = %v, want 2 entries", v.ToolsConfirmed)
	}
}

func TestBuildDelegationVerification_Confirmed(t *testing.T) {
	// Audit trail contains the expected destructive tool.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "get_session_info"}},
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "terminate_connection"}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionDestructive, "evt_del2", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: terminate_connection was confirmed")
	}
	if len(v.DestructiveConfirmed) != 1 || v.DestructiveConfirmed[0] != "terminate_connection" {
		t.Errorf("DestructiveConfirmed = %v, want [terminate_connection]", v.DestructiveConfirmed)
	}
}

func TestBuildDelegationVerification_ReadDelegation_NeverMismatchFromToolAbsence(t *testing.T) {
	// A read delegation with no tools called, and no agent_reasoning claiming
	// otherwise, is never a mismatch — the write/destructive-absence check never
	// applies to reads, and there's nothing narrated to be unconfirmed.
	srv := serveFakeEvents(t, []Event{})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionRead, "evt_del3", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: no tools called and nothing narrated")
	}
}

func TestBuildDelegationVerification_MismatchFromNarration(t *testing.T) {
	// A read delegation where the agent's own reasoning names a tool it never
	// actually called (no matching tool_execution event), and no policy denial
	// explains the absence — this is the live-discovered gap: previously invisible
	// on reads since Mismatch was only ever computed for write/destructive.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeAgentReasoning, AgentReasoning: &AgentReasoning{
			Reasoning: "Let me check the logs", ToolCalls: []string{"read_pg_log"},
		}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionRead, "evt_del7", "postgres_database_agent")

	if !v.Mismatch {
		t.Error("Mismatch = false, want true: narrated tool call with no matching execution and no policy denial")
	}
	if len(v.NarratedNotConfirmed) != 1 || v.NarratedNotConfirmed[0] != "read_pg_log" {
		t.Errorf("NarratedNotConfirmed = %v, want [read_pg_log]", v.NarratedNotConfirmed)
	}
}

func TestBuildDelegationVerification_NarrationConfirmed_NoMismatch(t *testing.T) {
	// The narrated tool call DID produce a matching tool_execution event — no mismatch.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "read_pg_log"}},
		{EventType: EventTypeAgentReasoning, AgentReasoning: &AgentReasoning{
			ToolCalls: []string{"read_pg_log"},
		}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionRead, "evt_del8", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: narrated tool call was actually confirmed")
	}
	if len(v.NarratedNotConfirmed) != 0 {
		t.Errorf("NarratedNotConfirmed = %v, want empty", v.NarratedNotConfirmed)
	}
}

func TestBuildDelegationVerification_SuppressedByPolicyDenial(t *testing.T) {
	// The narrated tool call has no matching tool_execution event, but a policy
	// denial in the same trace explains why — this is policy working correctly,
	// not fabrication, so it must NOT be flagged as a mismatch.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeAgentReasoning, AgentReasoning: &AgentReasoning{
			ToolCalls: []string{"get_nodes"},
		}},
		{EventType: EventTypePolicyDecision, PolicyDecision: &PolicyDecision{
			Effect: "deny", Action: "read", Message: "purpose required",
		}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionRead, "evt_del9", "k8s_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: policy denial explains the narrated-but-unconfirmed call")
	}
	if len(v.NarratedNotConfirmed) != 0 {
		t.Errorf("NarratedNotConfirmed = %v, want empty when suppressed by policy denial", v.NarratedNotConfirmed)
	}
}

func TestBuildDelegationVerification_NarrationMismatch_UnconditionalOnActionClass(t *testing.T) {
	// The narration check fires regardless of ActionClass — including destructive,
	// where it's additive to (not a replacement for) the existing write/destructive
	// check. Here the destructive tool WAS confirmed (no write/destructive mismatch),
	// but a separate narrated tool was not — still a mismatch, via narration alone.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "terminate_connection"}},
		{EventType: EventTypeAgentReasoning, AgentReasoning: &AgentReasoning{
			ToolCalls: []string{"terminate_connection", "get_session_info"},
		}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionDestructive, "evt_del10", "postgres_database_agent")

	if !v.Mismatch {
		t.Error("Mismatch = false, want true: get_session_info was narrated but never executed")
	}
	if len(v.DestructiveConfirmed) != 1 {
		t.Errorf("DestructiveConfirmed = %v, want [terminate_connection] — the write/destructive check should still pass independently", v.DestructiveConfirmed)
	}
	if len(v.NarratedNotConfirmed) != 1 || v.NarratedNotConfirmed[0] != "get_session_info" {
		t.Errorf("NarratedNotConfirmed = %v, want [get_session_info]", v.NarratedNotConfirmed)
	}
}

func TestBuildDelegationVerification_NoAuditURL(t *testing.T) {
	// Empty auditURL: returns zero-value verification without mismatch.
	v := buildDelegationVerification("", "", "tr_test", time.Now(), ActionDestructive, "evt_del4", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: no auditURL means no verification possible")
	}
	if len(v.ToolsConfirmed) != 0 {
		t.Errorf("ToolsConfirmed = %v, want empty when auditURL is unset", v.ToolsConfirmed)
	}
}

func TestBuildDelegationVerification_WriteAction_Mismatch(t *testing.T) {
	// Write delegation with only a read tool confirmed — no write-or-stronger tool.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "check_connection"}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionWrite, "evt_del5", "postgres_database_agent")

	if !v.Mismatch {
		t.Error("Mismatch = false, want true: write delegation with no write-or-stronger tool confirmed")
	}
	if v.ActionClass != ActionWrite {
		t.Errorf("ActionClass = %q, want %q", v.ActionClass, ActionWrite)
	}
}

func TestBuildDelegationVerification_WriteAction_ConfirmedWrite(t *testing.T) {
	// Write delegation confirmed by a write tool — no mismatch.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "cancel_query"}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionWrite, "evt_del6", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: cancel_query (write) satisfies a write delegation")
	}
	if len(v.WriteConfirmed) != 1 || v.WriteConfirmed[0] != "cancel_query" {
		t.Errorf("WriteConfirmed = %v, want [cancel_query]", v.WriteConfirmed)
	}
}

func TestBuildDelegationVerification_WriteAction_ConfirmedDestructive(t *testing.T) {
	// Write delegation confirmed by a destructive tool — destructive satisfies write, no mismatch.
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "terminate_connection"}},
	})

	v := buildDelegationVerification(srv.URL, "", "tr_test", time.Now().Add(-time.Minute), ActionWrite, "evt_del7", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: terminate_connection (destructive) satisfies a write delegation")
	}
	if len(v.DestructiveConfirmed) != 1 || v.DestructiveConfirmed[0] != "terminate_connection" {
		t.Errorf("DestructiveConfirmed = %v, want [terminate_connection]", v.DestructiveConfirmed)
	}
}

func TestFormatVerificationBlock_Mismatch_Destructive(t *testing.T) {
	v := &DelegationVerification{
		DelegationEventID: "evt_abc",
		Agent:             "postgres_database_agent",
		ActionClass:       ActionDestructive,
		ToolsConfirmed:    []string{"get_session_info"},
		Mismatch:          true,
	}
	block := formatVerificationBlock(v)

	if !strings.Contains(block, "MISMATCH") {
		t.Errorf("block missing MISMATCH: %s", block)
	}
	if !strings.Contains(block, "destructive") {
		t.Errorf("block missing action_class 'destructive': %s", block)
	}
	if !strings.Contains(block, "evt_abc") {
		t.Errorf("block missing delegation event ID: %s", block)
	}
	if !strings.Contains(block, "Do NOT claim success") {
		t.Errorf("block missing 'Do NOT claim success' instruction: %s", block)
	}
}

func TestFormatVerificationBlock_Mismatch_Write(t *testing.T) {
	v := &DelegationVerification{
		DelegationEventID: "evt_wri",
		Agent:             "postgres_database_agent",
		ActionClass:       ActionWrite,
		ToolsConfirmed:    []string{"check_connection"},
		Mismatch:          true,
	}
	block := formatVerificationBlock(v)

	if !strings.Contains(block, "MISMATCH") {
		t.Errorf("block missing MISMATCH: %s", block)
	}
	if !strings.Contains(block, "write") {
		t.Errorf("block missing action_class 'write': %s", block)
	}
	if !strings.Contains(block, "Do NOT claim success") {
		t.Errorf("block missing 'Do NOT claim success' instruction: %s", block)
	}
}

// TestBuildDelegationVerification_Exported verifies that the exported
// BuildDelegationVerification delegates to the unexported implementation.
// The gateway package uses the exported form; this test guards the contract.
func TestBuildDelegationVerification_Exported(t *testing.T) {
	srv := serveFakeEvents(t, []Event{
		{EventType: EventTypeToolExecution, Tool: &ToolExecution{Name: "terminate_connection"}},
	})

	// Exported form should return the same result as the unexported form.
	v := BuildDelegationVerification(srv.URL, "", "tr_exp", time.Now().Add(-time.Minute), ActionDestructive, "evt_exp1", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: terminate_connection is destructive → satisfies ActionDestructive")
	}
	if len(v.DestructiveConfirmed) != 1 || v.DestructiveConfirmed[0] != "terminate_connection" {
		t.Errorf("DestructiveConfirmed = %v, want [terminate_connection]", v.DestructiveConfirmed)
	}
}

func TestFormatVerificationBlock_NarratedNotConfirmed(t *testing.T) {
	v := &DelegationVerification{
		DelegationEventID:    "evt_nar",
		Agent:                "postgres_database_agent",
		ActionClass:          ActionRead,
		ToolsConfirmed:       nil,
		Mismatch:             true,
		NarratedNotConfirmed: []string{"read_pg_log"},
	}
	block := formatVerificationBlock(v)

	if !strings.Contains(block, "NARRATED BUT NOT CONFIRMED") {
		t.Errorf("block missing narration-specific signal: %s", block)
	}
	if !strings.Contains(block, "read_pg_log") {
		t.Errorf("block missing the unconfirmed tool name: %s", block)
	}
	if strings.Contains(block, "MISMATCH:") {
		t.Errorf("narration case should use its own distinct signal, not the write/destructive MISMATCH wording: %s", block)
	}
}

func TestFormatVerificationBlock_Clean(t *testing.T) {
	v := &DelegationVerification{
		DelegationEventID:    "evt_def",
		Agent:                "postgres_database_agent",
		ToolsConfirmed:       []string{"terminate_connection"},
		DestructiveConfirmed: []string{"terminate_connection"},
		Mismatch:             false,
	}
	block := formatVerificationBlock(v)

	if strings.Contains(block, "MISMATCH") {
		t.Errorf("clean verification block should not contain MISMATCH: %s", block)
	}
	if !strings.Contains(block, "terminate_connection") {
		t.Errorf("block missing confirmed tool name: %s", block)
	}
	if !strings.Contains(block, "VERIFICATION CLEAN") {
		t.Errorf("clean block missing explicit 'VERIFICATION CLEAN' signal: %s", block)
	}
}

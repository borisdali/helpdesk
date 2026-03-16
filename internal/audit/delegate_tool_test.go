package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveFakeEvents returns an httptest.Server that responds to
// GET /v1/events with the given events JSON-encoded.
func serveFakeEvents(t *testing.T, events []Event) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/events") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events) //nolint:errcheck
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

	v := buildDelegationVerification(srv.URL, "tr_test", time.Now().Add(-time.Minute), ActionDestructive, "evt_del1", "postgres_database_agent")

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

	v := buildDelegationVerification(srv.URL, "tr_test", time.Now().Add(-time.Minute), ActionDestructive, "evt_del2", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: terminate_connection was confirmed")
	}
	if len(v.DestructiveConfirmed) != 1 || v.DestructiveConfirmed[0] != "terminate_connection" {
		t.Errorf("DestructiveConfirmed = %v, want [terminate_connection]", v.DestructiveConfirmed)
	}
}

func TestBuildDelegationVerification_ReadDelegation_NeverMismatch(t *testing.T) {
	// A read delegation with no tools called is never a mismatch.
	srv := serveFakeEvents(t, []Event{})

	v := buildDelegationVerification(srv.URL, "tr_test", time.Now().Add(-time.Minute), ActionRead, "evt_del3", "postgres_database_agent")

	if v.Mismatch {
		t.Error("Mismatch = true, want false: read delegations are never a mismatch")
	}
}

func TestBuildDelegationVerification_NoAuditURL(t *testing.T) {
	// Empty auditURL: returns zero-value verification without mismatch.
	v := buildDelegationVerification("", "tr_test", time.Now(), ActionDestructive, "evt_del4", "postgres_database_agent")

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

	v := buildDelegationVerification(srv.URL, "tr_test", time.Now().Add(-time.Minute), ActionWrite, "evt_del5", "postgres_database_agent")

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

	v := buildDelegationVerification(srv.URL, "tr_test", time.Now().Add(-time.Minute), ActionWrite, "evt_del6", "postgres_database_agent")

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

	v := buildDelegationVerification(srv.URL, "tr_test", time.Now().Add(-time.Minute), ActionWrite, "evt_del7", "postgres_database_agent")

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

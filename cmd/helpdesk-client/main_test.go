package main

import (
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/client"
)

func TestFormatVerification_NoTools(t *testing.T) {
	v := &client.TraceVerification{}
	got := formatVerification(v)
	if !strings.Contains(got, "no tool executions confirmed") {
		t.Errorf("formatVerification() = %q, want 'no tool executions confirmed'", got)
	}
	if !strings.Contains(got, "⚠") {
		t.Errorf("formatVerification() = %q, want warning symbol", got)
	}
}

func TestFormatVerification_ReadOnly(t *testing.T) {
	v := &client.TraceVerification{
		ToolsConfirmed: []client.ConfirmedTool{{Name: "check_connection", ActionClass: "read"}},
	}
	got := formatVerification(v)
	if !strings.Contains(got, "check_connection (read)") {
		t.Errorf("formatVerification() = %q, missing tool name", got)
	}
	// No mutation suffix for read-only.
	if strings.Contains(got, "confirmed ✓") {
		t.Errorf("formatVerification() = %q, unexpected mutation suffix for read-only", got)
	}
}

func TestFormatVerification_WriteConfirmed(t *testing.T) {
	v := &client.TraceVerification{
		ToolsConfirmed: []client.ConfirmedTool{
			{Name: "cancel_query", ActionClass: "write"},
			{Name: "get_session_info", ActionClass: "read"},
		},
		WriteConfirmed: []string{"cancel_query"},
	}
	got := formatVerification(v)
	if !strings.Contains(got, "cancel_query (write)") {
		t.Errorf("formatVerification() = %q, missing write tool", got)
	}
	if !strings.Contains(got, "get_session_info (read)") {
		t.Errorf("formatVerification() = %q, missing read tool", got)
	}
	if !strings.Contains(got, "write confirmed ✓") {
		t.Errorf("formatVerification() = %q, want 'write confirmed ✓'", got)
	}
}

func TestFormatVerification_DestructiveConfirmed(t *testing.T) {
	v := &client.TraceVerification{
		ToolsConfirmed:       []client.ConfirmedTool{{Name: "terminate_connection", ActionClass: "destructive"}},
		DestructiveConfirmed: []string{"terminate_connection"},
	}
	got := formatVerification(v)
	if !strings.Contains(got, "terminate_connection (destructive)") {
		t.Errorf("formatVerification() = %q, missing destructive tool", got)
	}
	if !strings.Contains(got, "destructive confirmed ✓") {
		t.Errorf("formatVerification() = %q, want 'destructive confirmed ✓'", got)
	}
}

func TestFormatVerification_MultipleTools(t *testing.T) {
	v := &client.TraceVerification{
		ToolsConfirmed: []client.ConfirmedTool{
			{Name: "get_session_info", ActionClass: "read"},
			{Name: "cancel_query", ActionClass: "write"},
			{Name: "check_connection", ActionClass: "read"},
		},
		WriteConfirmed: []string{"cancel_query"},
	}
	got := formatVerification(v)
	// All tools should appear in the output.
	for _, want := range []string{"get_session_info", "cancel_query", "check_connection"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatVerification() = %q, missing %q", got, want)
		}
	}
}

// TestFormatVerification_Layer2Label pins the "[Layer 2]" label sourced from
// audit.LayerDelegationVerification on both the no-tools-confirmed warning
// branch and the confirmed-tools summary branch — the existing tests above
// only assert on substrings that predate the label, so none of them would
// catch a regression where the label was dropped or hand-typed differently.
func TestFormatVerification_Layer2Label(t *testing.T) {
	noTools := formatVerification(&client.TraceVerification{})
	if !strings.Contains(noTools, "["+audit.LayerDelegationVerification+"]") {
		t.Errorf("formatVerification() = %q, want it to contain the %q label", noTools, "["+audit.LayerDelegationVerification+"]")
	}

	confirmed := formatVerification(&client.TraceVerification{
		ToolsConfirmed: []client.ConfirmedTool{{Name: "check_connection", ActionClass: "read"}},
	})
	if !strings.Contains(confirmed, "["+audit.LayerDelegationVerification+"]") {
		t.Errorf("formatVerification() = %q, want it to contain the %q label", confirmed, "["+audit.LayerDelegationVerification+"]")
	}
}

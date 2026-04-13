//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

// TestDBAgentToolCallSummary verifies that the database agent emits a
// tool_call_summary DataPart (Option C) in its A2A response artifacts.
//
// This test is the end-to-end guard for newToolCallCallbacks() in agentutil:
// it confirms that the ADK AfterEventCallback fires, accumulates tool names,
// and injects them as a structured DataPart. When this test passes,
// faulttest's structured tool evidence path (Option C) will work correctly.
// When it fails — due to an ADK API change, the callback not being wired,
// or a DataPart format mismatch — it surfaces before a full faulttest run.
func TestDBAgentToolCallSummary(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !isAgentReachable(cfg.DBAgentURL) {
		t.Skipf("DB agent not reachable at %s", cfg.DBAgentURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prompt := "Check the database connection and report the PostgreSQL version. " +
		"The connection_string is `" + cfg.ConnStr + "`."

	t.Logf("Sending direct A2A prompt to DB agent at %s", cfg.DBAgentURL)
	resp := testutil.SendPrompt(ctx, cfg.DBAgentURL, prompt)
	if resp.Error != nil {
		if strings.Contains(resp.Error.Error(), "no such host") ||
			strings.Contains(resp.Error.Error(), "lookup") {
			t.Skipf("DB agent uses Docker-internal hostname (run inside container or use gateway): %v", resp.Error)
		}
		t.Fatalf("SendPrompt failed: %v", resp.Error)
	}

	t.Logf("Response text (%d chars, %s): %s", len(resp.Text), resp.Duration, truncate(resp.Text, 200))

	// Primary assertion: the tool_call_summary DataPart must be present.
	// A nil ToolCalls means the DataPart was absent — agentutil's AfterEventCallback
	// did not fire or inject the summary. This is Option B (text fallback) territory.
	if resp.ToolCalls == nil {
		t.Fatal("ToolCalls is nil: tool_call_summary DataPart not found in A2A artifacts. " +
			"This means newToolCallCallbacks() did not emit the DataPart — " +
			"check that the AfterEventCallback is wired in agentutil.Serve*.")
	}

	if len(resp.ToolCalls) == 0 {
		t.Fatal("ToolCalls is present but empty: the DataPart was emitted with an empty tool list. " +
			"The agent completed without calling any tools, which is unexpected for a connection check prompt.")
	}

	t.Logf("Tool calls observed (%d):", len(resp.ToolCalls))
	for _, tc := range resp.ToolCalls {
		t.Logf("  %s (success=%v)", tc.Name, tc.Success)
	}

	// Verify at least one expected tool was called. The DB agent should always
	// invoke check_connection or get_database_info for a connection check prompt.
	expectedTools := []string{"check_connection", "get_database_info", "get_database_stats", "get_config_parameter"}
	found := false
	for _, tc := range resp.ToolCalls {
		for _, expected := range expectedTools {
			if tc.Name == expected {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		toolNames := make([]string, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			toolNames[i] = tc.Name
		}
		t.Errorf("None of the expected DB tools were called. Got: %v. Expected one of: %v",
			toolNames, expectedTools)
	}
}

package main

import (
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestCheckFabricationMismatch_EmitsCriticalAlert verifies that Analyze fires a
// "fabrication_mismatch" critical security alert when a delegation_verification
// event with Mismatch=true is processed.
func TestCheckFabricationMismatch_EmitsCriticalAlert(t *testing.T) {
	auditor := NewAuditor(Config{}, nil, nil)

	event := &audit.Event{
		EventID:   "gv_test001",
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeDelegationVerification,
		TraceID:   "tr_abc",
		Session:   audit.Session{ID: "tr_abc"},
		DelegationVerification: &audit.DelegationVerification{
			Agent:       "postgres_database_agent",
			ActionClass: audit.ActionDestructive,
			Mismatch:    true,
		},
	}

	auditor.Analyze(event)

	auditor.mu.Lock()
	alerts := auditor.securityAlerts
	auditor.mu.Unlock()

	var found *SecurityAlert
	for i := range alerts {
		if alerts[i].Type == "fabrication_mismatch" {
			found = &alerts[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected a fabrication_mismatch security alert; got none")
	}
	if found.Severity != string(AlertCritical) {
		t.Errorf("Severity = %q, want %q", found.Severity, AlertCritical)
	}
	if !strings.Contains(found.Message, "FABRICATION RISK") {
		t.Errorf("Message = %q, want to contain FABRICATION RISK", found.Message)
	}
}

// TestCheckFabricationMismatch_NoAlertOnCleanVerification verifies that a
// delegation_verification event with Mismatch=false does NOT trigger an alert.
func TestCheckFabricationMismatch_NoAlertOnCleanVerification(t *testing.T) {
	auditor := NewAuditor(Config{}, nil, nil)

	event := &audit.Event{
		EventID:   "gv_clean01",
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeDelegationVerification,
		TraceID:   "tr_clean",
		Session:   audit.Session{ID: "tr_clean"},
		DelegationVerification: &audit.DelegationVerification{
			Agent:       "postgres_database_agent",
			ActionClass: audit.ActionRead,
			Mismatch:    false,
		},
	}

	auditor.Analyze(event)

	auditor.mu.Lock()
	alerts := auditor.securityAlerts
	auditor.mu.Unlock()

	for _, a := range alerts {
		if a.Type == "fabrication_mismatch" {
			t.Error("unexpected fabrication_mismatch alert for clean verification")
		}
	}
}

// TestCheckFabricationMismatch_NoAlertOnOtherEventType verifies that non-verification
// events are not mistakenly classified as fabrication mismatches.
func TestCheckFabricationMismatch_NoAlertOnOtherEventType(t *testing.T) {
	auditor := NewAuditor(Config{}, nil, nil)

	event := &audit.Event{
		EventID:   "tool_001",
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeToolExecution,
		TraceID:   "tr_tool",
		Session:   audit.Session{ID: "tr_tool"},
		Tool: &audit.ToolExecution{
			Name:  "get_active_connections",
			Agent: "postgres_database_agent",
		},
		Outcome: &audit.Outcome{Status: "success"},
	}

	auditor.Analyze(event)

	auditor.mu.Lock()
	alerts := auditor.securityAlerts
	auditor.mu.Unlock()

	for _, a := range alerts {
		if a.Type == "fabrication_mismatch" {
			t.Error("unexpected fabrication_mismatch alert for non-verification event")
		}
	}
}

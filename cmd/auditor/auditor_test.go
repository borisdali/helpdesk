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

// TestCheckFabricationMismatch_NarrationOnly_EmitsWarningNotCritical verifies the
// severity tiering: a Mismatch caused only by a narrated-but-unconfirmed tool call
// (not by the write/destructive-absence check) fires a lower-severity
// "narrated_tool_not_confirmed" WARNING, not the CRITICAL "fabrication_mismatch"
// alert — a brand-new check with known false-positive vectors (policy denial,
// hallucinated tool names) shouldn't share the incident-webhook-triggering severity
// of the established write/destructive case until its false-positive rate is known.
func TestCheckFabricationMismatch_NarrationOnly_EmitsWarningNotCritical(t *testing.T) {
	auditor := NewAuditor(Config{}, nil, nil)

	event := &audit.Event{
		EventID:   "gv_narr001",
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeDelegationVerification,
		TraceID:   "tr_narr",
		Session:   audit.Session{ID: "tr_narr"},
		DelegationVerification: &audit.DelegationVerification{
			Agent:                "postgres_database_agent",
			ActionClass:          audit.ActionRead,
			Mismatch:             true,
			NarratedNotConfirmed: []string{"read_pg_log"},
		},
	}

	auditor.Analyze(event)

	auditor.mu.Lock()
	alerts := auditor.securityAlerts
	auditor.mu.Unlock()

	var fabrication, narration *SecurityAlert
	for i := range alerts {
		switch alerts[i].Type {
		case "fabrication_mismatch":
			fabrication = &alerts[i]
		case "narrated_tool_not_confirmed":
			narration = &alerts[i]
		}
	}
	if fabrication != nil {
		t.Errorf("unexpected CRITICAL fabrication_mismatch alert for a narration-only mismatch: %+v", fabrication)
	}
	if narration == nil {
		t.Fatal("expected a narrated_tool_not_confirmed security alert; got none")
	}
	if narration.Severity != string(AlertWarning) {
		t.Errorf("Severity = %q, want %q", narration.Severity, AlertWarning)
	}
}

// TestCheckFabricationMismatch_WriteDestructiveAbsence_StaysCriticalEvenWithNarration
// verifies that when BOTH the write/destructive-absence check and the narration check
// would fire on the same event, the existing CRITICAL write/destructive alerting is
// not weakened — exactly one alert is emitted, at CRITICAL severity.
func TestCheckFabricationMismatch_WriteDestructiveAbsence_StaysCriticalEvenWithNarration(t *testing.T) {
	auditor := NewAuditor(Config{}, nil, nil)

	event := &audit.Event{
		EventID:   "gv_both001",
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeDelegationVerification,
		TraceID:   "tr_both",
		Session:   audit.Session{ID: "tr_both"},
		DelegationVerification: &audit.DelegationVerification{
			Agent:                "postgres_database_agent",
			ActionClass:          audit.ActionDestructive,
			Mismatch:             true,
			NarratedNotConfirmed: []string{"get_session_info"}, // also narrated a call that never ran
			// DestructiveConfirmed left empty — no destructive tool executed either.
		},
	}

	auditor.Analyze(event)

	auditor.mu.Lock()
	alerts := auditor.securityAlerts
	auditor.mu.Unlock()

	var fabrication, narration *SecurityAlert
	for i := range alerts {
		switch alerts[i].Type {
		case "fabrication_mismatch":
			fabrication = &alerts[i]
		case "narrated_tool_not_confirmed":
			narration = &alerts[i]
		}
	}
	if fabrication == nil {
		t.Fatal("expected a CRITICAL fabrication_mismatch alert; got none")
	}
	if fabrication.Severity != string(AlertCritical) {
		t.Errorf("Severity = %q, want %q", fabrication.Severity, AlertCritical)
	}
	if narration != nil {
		t.Errorf("expected no separate narrated_tool_not_confirmed alert when the write/destructive case already fired: %+v", narration)
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

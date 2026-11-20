package main

import (
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// The metadata the agent receives says when the incident began, as an instant it
// can parse back, and says nothing when the gateway did not know.
func TestRecoveryApprovalMeta_CarriesTheIncidentStart(t *testing.T) {
	ap := &audit.RecoveryApproval{Type: "time", Value: "v", PlanDigest: "d", ApprovedBy: "a", Acknowledged: true,
		IncidentStartedAt: time.Date(2026, 11, 17, 22, 52, 5, 0, time.UTC)}
	m := recoveryApprovalMeta(ap)
	got, _ := m["incident_started_at"].(string)
	if parsed, err := time.Parse(time.RFC3339Nano, got); err != nil || !parsed.Equal(ap.IncidentStartedAt) {
		t.Errorf("incident_started_at = %q (%v)", got, err)
	}
	ap.IncidentStartedAt = time.Time{}
	if m := recoveryApprovalMeta(ap); m["incident_started_at"] != nil {
		t.Errorf("an unknown start was sent: %v", m["incident_started_at"])
	}
}

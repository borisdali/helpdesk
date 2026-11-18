package audit

import (
	"testing"
	"time"
)

// The approval carries when its incident began, in the A2A metadata, so the
// restore's budget check can count the wait the ledger no longer shows as open.
func TestParseRecoveryApproval_IncidentStart(t *testing.T) {
	base := map[string]any{"type": "time", "value": "2026-11-17T22:52:08Z", "plan_digest": "abc", "approved_by": "alice", "acknowledged": true}
	with := func(v any) map[string]any {
		m := map[string]any{}
		for k, x := range base {
			m[k] = x
		}
		m["incident_started_at"] = v
		return m
	}
	want := time.Date(2026, 11, 17, 22, 52, 5, 123, time.UTC)
	if a := parseRecoveryApproval(with(want.Format(time.RFC3339Nano))); a == nil || !a.IncidentStartedAt.Equal(want) {
		t.Errorf("parsed = %+v", a)
	}
	// An offset is read and kept as the same instant, in UTC.
	if a := parseRecoveryApproval(with("2026-11-17T17:52:05-05:00")); a == nil || !a.IncidentStartedAt.Equal(time.Date(2026, 11, 17, 22, 52, 5, 0, time.UTC)) || a.IncidentStartedAt.Location() != time.UTC {
		t.Errorf("offset: %+v", a)
	}
	// Absent or unreadable: no start, and the approval is otherwise as it was.
	for name, v := range map[string]any{"missing": nil, "garbage": "yesterday", "number": 5} {
		a := parseRecoveryApproval(with(v))
		if a == nil || !a.IncidentStartedAt.IsZero() || !a.Approved() {
			t.Errorf("%s: %+v", name, a)
		}
	}
}

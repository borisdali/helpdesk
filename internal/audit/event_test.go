package audit

import (
	"encoding/json"
	"testing"
	"time"
)

// TestMilliseconds_RoundTrip is a regression test for a real bug found live
// (2026-10-11): ToolExecution.Duration/Outcome.Duration used to be plain
// time.Duration fields tagged json:"duration_ms" — Go's default JSON
// encoding of time.Duration is its raw nanosecond count, which silently
// mislabeled that field name. A real ~5.77s tool call (create_incident_bundle)
// produced duration_ms=5774637750 — nanoseconds, not milliseconds — which
// overflowed the int4 column auditd stores it in
// ("5774637750 is greater than maximum value for int4"). Milliseconds makes
// the field's actual unit match its name.
func TestMilliseconds_RoundTrip(t *testing.T) {
	d := 5774*time.Millisecond + 637750*time.Nanosecond // ~5.77s, the real incident's own duration
	ms := MillisecondsFromDuration(d)

	const wantMs = int64(5774) // sub-millisecond precision is intentionally dropped
	if int64(ms) != wantMs {
		t.Fatalf("MillisecondsFromDuration(%v) = %d, want %d", d, int64(ms), wantMs)
	}

	// The whole bug was a wrong JSON encoding — assert on the actual bytes,
	// not just the Go-side int64 value.
	data, err := json.Marshal(ms)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != "5774" {
		t.Errorf("json.Marshal(Milliseconds) = %s, want 5774 (a real millisecond count, not 5774637750 nanoseconds)", data)
	}

	got := ms.Duration()
	want := 5774 * time.Millisecond
	if got != want {
		t.Errorf("Milliseconds(%d).Duration() = %v, want %v", ms, got, want)
	}
}

// TestMilliseconds_ToolExecutionAndOutcome_JSONShape proves the fix at the
// level it actually broke: encoding a full ToolExecution/Outcome pair (the
// exact types whose duration_ms field overflowed int4 live) produces a
// plain, small JSON number — not a value in the billions that would
// overflow a 32-bit column.
func TestMilliseconds_ToolExecutionAndOutcome_JSONShape(t *testing.T) {
	d := 5774637750 * time.Nanosecond // the exact real duration from the live incident

	tool := ToolExecution{Name: "create_incident_bundle", Duration: MillisecondsFromDuration(d)}
	toolJSON, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal(ToolExecution): %v", err)
	}
	var toolDecoded map[string]any
	if err := json.Unmarshal(toolJSON, &toolDecoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := toolDecoded["duration_ms"]; got != 5774.0 {
		t.Errorf("ToolExecution duration_ms = %v, want 5774 (not ~5.77 billion nanoseconds)", got)
	}

	outcome := Outcome{Status: "success", Duration: MillisecondsFromDuration(d)}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("Marshal(Outcome): %v", err)
	}
	var outcomeDecoded map[string]any
	if err := json.Unmarshal(outcomeJSON, &outcomeDecoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := outcomeDecoded["duration_ms"]; got != 5774.0 {
		t.Errorf("Outcome duration_ms = %v, want 5774 (not ~5.77 billion nanoseconds)", got)
	}
}

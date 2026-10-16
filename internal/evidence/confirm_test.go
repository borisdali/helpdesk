package evidence

import (
	"testing"

	"helpdesk/internal/audit"
)

func reportWithPrimary(confidence float64, quote string) *audit.DiagnosticReport {
	return &audit.DiagnosticReport{
		Hypotheses: []audit.DiagnosticHypothesis{
			{IsPrimary: false, Confidence: 0.10, Evidence: "unrelated rejected hypothesis"},
			{IsPrimary: true, Confidence: confidence, Evidence: quote},
		},
	}
}

func TestPrimaryConfidence(t *testing.T) {
	if got := primaryConfidence(nil); got != 0 {
		t.Errorf("nil report: primaryConfidence() = %v, want 0", got)
	}
	if got := primaryConfidence(&audit.DiagnosticReport{}); got != 0 {
		t.Errorf("no hypotheses: primaryConfidence() = %v, want 0", got)
	}
	if got := primaryConfidence(&audit.DiagnosticReport{
		Hypotheses: []audit.DiagnosticHypothesis{{IsPrimary: false, Confidence: 0.9}},
	}); got != 0 {
		t.Errorf("no hypothesis marked primary: primaryConfidence() = %v, want 0", got)
	}
	if got := primaryConfidence(reportWithPrimary(0.85, "quote")); got != 0.85 {
		t.Errorf("primaryConfidence() = %v, want 0.85", got)
	}
}

func TestEvidenceQuoteContainsValue(t *testing.T) {
	cases := []struct {
		name string
		h    HopOutcome
		ev   audit.ObjectiveEvidence
		want bool
	}{
		{
			name: "integer-valued float64 matched as integer string",
			h:    HopOutcome{Report: reportWithPrimary(0.95, "slot_name | replica_slot\nactive | f\nlag_bytes | 95475440")},
			ev:   audit.ObjectiveEvidence{Value: float64(95475440)},
			want: true,
		},
		{
			name: "value not present in quote",
			h:    HopOutcome{Report: reportWithPrimary(0.95, "everything looks healthy")},
			ev:   audit.ObjectiveEvidence{Value: float64(95475440)},
			want: false,
		},
		{
			name: "string value substring match, case-insensitive",
			h:    HopOutcome{Report: reportWithPrimary(0.9, "event reason: FAILEDSCHEDULING (insufficient cpu)")},
			ev:   audit.ObjectiveEvidence{Value: "FailedScheduling"},
			want: true,
		},
		{
			name: "nil Value never matches",
			h:    HopOutcome{Report: reportWithPrimary(0.9, "anything at all")},
			ev:   audit.ObjectiveEvidence{Value: nil},
			want: false,
		},
		{
			name: "nil report never matches",
			h:    HopOutcome{Report: nil},
			ev:   audit.ObjectiveEvidence{Value: float64(1)},
			want: false,
		},
		{
			name: "empty evidence quote never matches",
			h:    HopOutcome{Report: reportWithPrimary(0.9, "")},
			ev:   audit.ObjectiveEvidence{Value: float64(1)},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evidenceQuoteContainsValue(tc.h, tc.ev)
			if got != tc.want {
				t.Errorf("evidenceQuoteContainsValue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResourceNamedInQuote(t *testing.T) {
	cases := []struct {
		name string
		h    HopOutcome
		ev   audit.ObjectiveEvidence
		want bool
	}{
		{
			name: "resource present, case-insensitive",
			h:    HopOutcome{RawText: "The Replication Slot replica_slot is inactive."},
			ev:   audit.ObjectiveEvidence{Resource: "REPLICA_SLOT"},
			want: true,
		},
		{
			name: "resource absent",
			h:    HopOutcome{RawText: "everything looks healthy"},
			ev:   audit.ObjectiveEvidence{Resource: "replica_slot"},
			want: false,
		},
		{
			name: "empty resource never confirms",
			h:    HopOutcome{RawText: "replica_slot is mentioned right here"},
			ev:   audit.ObjectiveEvidence{Resource: ""},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourceNamedInQuote(tc.h, tc.ev)
			if got != tc.want {
				t.Errorf("resourceNamedInQuote() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfirmed_DefaultsToEvidenceQuoteContainsValue(t *testing.T) {
	h := HopOutcome{Report: reportWithPrimary(0.95, "lag_bytes | 95475440")}
	ev := audit.ObjectiveEvidence{Value: float64(95475440)} // no ConfirmationProbe set
	confirmed, err := Confirmed(h, ev)
	if err != nil {
		t.Fatalf("Confirmed: %v", err)
	}
	if !confirmed {
		t.Error("want confirmed=true via default probe")
	}
}

func TestConfirmed_ExplicitProbeAndThreshold(t *testing.T) {
	h := HopOutcome{Report: reportWithPrimary(0.8, "some quote")}
	ev := audit.ObjectiveEvidence{
		ConfirmationProbe: "primary_confidence", ConfirmationOperator: ">=", ConfirmationThreshold: 0.6,
	}
	confirmed, err := Confirmed(h, ev)
	if err != nil {
		t.Fatalf("Confirmed: %v", err)
	}
	if !confirmed {
		t.Error("want confirmed=true — 0.8 >= 0.6")
	}

	ev.ConfirmationThreshold = 0.95
	confirmed, err = Confirmed(h, ev)
	if err != nil {
		t.Fatalf("Confirmed: %v", err)
	}
	if confirmed {
		t.Error("want confirmed=false — 0.8 < 0.95")
	}
}

func TestConfirmed_UnknownProbe_ReturnsError(t *testing.T) {
	h := HopOutcome{Report: reportWithPrimary(0.95, "anything")}
	ev := audit.ObjectiveEvidence{ConfirmationProbe: "does_not_exist"}
	if _, err := Confirmed(h, ev); err == nil {
		t.Fatal("expected error for unknown confirmation probe")
	}
}

func TestValueStrings(t *testing.T) {
	if got := valueStrings(float64(95475440)); len(got) == 0 || got[0] != "95475440" {
		t.Errorf("valueStrings(95475440.0) = %v, want first form 95475440", got)
	}
	if got := valueStrings(float64(0.5)); len(got) == 0 {
		t.Errorf("valueStrings(0.5) = %v, want at least one form", got)
	}
	if got := valueStrings(true); len(got) != 1 || got[0] != "true" {
		t.Errorf("valueStrings(true) = %v, want [true]", got)
	}
	if got := valueStrings("FailedScheduling"); len(got) != 1 || got[0] != "FailedScheduling" {
		t.Errorf("valueStrings(%q) = %v, want passthrough", "FailedScheduling", got)
	}
}

func TestNormalizeQuote(t *testing.T) {
	for _, q := range []string{`backup_stale: true`, `backup_stale=true`, `"backup_stale":true`, `\"backup_stale\": TRUE`} {
		if got := normalizeQuote(q); got != "backup_stale:true" {
			t.Errorf("normalizeQuote(%q) = %q, want backup_stale:true", q, got)
		}
	}
}

func TestEvidenceQuoteContainsAny(t *testing.T) {
	ev := audit.ObjectiveEvidence{Value: true, ConfirmationProbe: "evidence_quote_contains_any",
		ConfirmationTerms: []string{"broken", "not in the repo"}}
	cases := []struct {
		name string
		h    HopOutcome
		want bool
	}{
		{"term in quote, case-insensitive", HopOutcome{Report: reportWithPrimary(1, "wal_continuity.status='BROKEN'")}, true},
		{"second term", HopOutcome{Report: reportWithPrimary(1, "the WAL backup X needs is not in the repo")}, true},
		{"fired value alone does not count", HopOutcome{Report: reportWithPrimary(1, "consistency_reachable: true")}, false},
		{"formatting normalized", HopOutcome{Report: reportWithPrimary(1, "detail=\"... is NOT in\nthe repo\"")}, true},
		{"no term, no value", HopOutcome{Report: reportWithPrimary(1, "wal_continuity.status: continuous")}, false},
		{"term only outside the EVIDENCE quote", HopOutcome{Report: reportWithPrimary(1, "status_code: 0"), RawText: "the chain is broken"}, false},
		{"term only in a non-primary hypothesis", HopOutcome{Report: &audit.DiagnosticReport{Hypotheses: []audit.DiagnosticHypothesis{
			{IsPrimary: false, Evidence: "broken"}, {IsPrimary: true, Evidence: "status_code: 0"}}}}, false},
		{"no report", HopOutcome{RawText: "broken"}, false},
	}
	for _, c := range cases {
		got, err := Confirmed(c.h, ev)
		if err != nil {
			t.Fatalf("%s: Confirmed: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: Confirmed = %v, want %v", c.name, got, c.want)
		}
	}
}

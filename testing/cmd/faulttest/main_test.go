package main

import (
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/testing/testutil"
)

// TestPrintFabricationAndEvidenceWarnings_Mismatch proves the live FABRICATION
// RISK line and its [Layer 2] label print and set EvalResult.Mismatch when
// resp.Mismatch is true — closing a gap where this print path (previously
// inline in cmdRun, a live-agent-calling loop with no unit coverage at all)
// had never been exercised by a test.
func TestPrintFabricationAndEvidenceWarnings_Mismatch(t *testing.T) {
	resp := testutil.AgentResponse{Mismatch: true}
	var evalResult EvalResult

	out := captureStdout(func() {
		printFabricationAndEvidenceWarnings(resp, &evalResult)
	})

	if !strings.Contains(out, "FABRICATION RISK") {
		t.Errorf("output = %q, want FABRICATION RISK", out)
	}
	if !strings.Contains(out, "["+audit.LayerDelegationVerification+"]") {
		t.Errorf("output = %q, want the %q label", out, "["+audit.LayerDelegationVerification+"]")
	}
	if !evalResult.Mismatch {
		t.Error("evalResult.Mismatch = false, want true")
	}
}

// TestPrintFabricationAndEvidenceWarnings_NoMismatch is the inverse case —
// resp.Mismatch=false must not print or set anything.
func TestPrintFabricationAndEvidenceWarnings_NoMismatch(t *testing.T) {
	resp := testutil.AgentResponse{}
	var evalResult EvalResult

	out := captureStdout(func() {
		printFabricationAndEvidenceWarnings(resp, &evalResult)
	})

	if out != "" {
		t.Errorf("output = %q, want empty for a clean response", out)
	}
	if evalResult.Mismatch {
		t.Error("evalResult.Mismatch = true, want false")
	}
}

// TestPrintFabricationAndEvidenceWarnings_UnverifiedEvidencePrimary proves
// the primary UNVERIFIED EVIDENCE line, its [Layer 3] label, and each quote
// print, and that EvalResult carries both the flag and the quotes through.
func TestPrintFabricationAndEvidenceWarnings_UnverifiedEvidencePrimary(t *testing.T) {
	resp := testutil.AgentResponse{
		UnverifiedEvidence: []string{"pg_stat_activity shows 3 idle connections"},
	}
	var evalResult EvalResult

	out := captureStdout(func() {
		printFabricationAndEvidenceWarnings(resp, &evalResult)
	})

	if !strings.Contains(out, "UNVERIFIED EVIDENCE (primary, non-blocking)") {
		t.Errorf("output = %q, want primary UNVERIFIED EVIDENCE line", out)
	}
	if !strings.Contains(out, "["+audit.LayerContentProvenance+"]") {
		t.Errorf("output = %q, want the %q label", out, "["+audit.LayerContentProvenance+"]")
	}
	if !strings.Contains(out, "pg_stat_activity shows 3 idle connections") {
		t.Errorf("output = %q, want the flagged quote to be printed", out)
	}
	if !evalResult.UnverifiedEvidence {
		t.Error("evalResult.UnverifiedEvidence = false, want true")
	}
	if len(evalResult.UnverifiedEvidenceQuotes) != 1 || evalResult.UnverifiedEvidenceQuotes[0] != "pg_stat_activity shows 3 idle connections" {
		t.Errorf("evalResult.UnverifiedEvidenceQuotes = %v, want the one flagged quote", evalResult.UnverifiedEvidenceQuotes)
	}
}

// TestPrintFabricationAndEvidenceWarnings_UnverifiedEvidenceSecondary mirrors
// the primary case above for the secondary (rejected-hypothesis) quotes,
// proving it uses the lowercase "unverified evidence (secondary...)" label
// and does not set the primary EvalResult fields.
func TestPrintFabricationAndEvidenceWarnings_UnverifiedEvidenceSecondary(t *testing.T) {
	resp := testutil.AgentResponse{
		UnverifiedEvidenceSecondary: []string{"disk usage at 95%"},
	}
	var evalResult EvalResult

	out := captureStdout(func() {
		printFabricationAndEvidenceWarnings(resp, &evalResult)
	})

	if !strings.Contains(out, "unverified evidence (secondary, non-blocking)") {
		t.Errorf("output = %q, want secondary unverified evidence line", out)
	}
	if !strings.Contains(out, "["+audit.LayerContentProvenance+"]") {
		t.Errorf("output = %q, want the %q label", out, "["+audit.LayerContentProvenance+"]")
	}
	if !strings.Contains(out, "disk usage at 95%") {
		t.Errorf("output = %q, want the flagged secondary quote to be printed", out)
	}
	if !evalResult.UnverifiedEvidenceSecondary {
		t.Error("evalResult.UnverifiedEvidenceSecondary = false, want true")
	}
	if evalResult.UnverifiedEvidence {
		t.Error("evalResult.UnverifiedEvidence = true, want false (secondary quotes must not set the primary flag)")
	}
}

// TestPrintFabricationAndEvidenceWarnings_AllThreeIndependent proves the
// three warning kinds are independently gated, not accidentally coupled —
// mirrors the same "independence" property vault.go's warning sections are
// already tested for (TestPrintJourneyDetail_MismatchWarning_NoDriftWarning).
func TestPrintFabricationAndEvidenceWarnings_AllThreeIndependent(t *testing.T) {
	resp := testutil.AgentResponse{
		Mismatch:           true,
		UnverifiedEvidence: []string{"quote one"},
	}
	var evalResult EvalResult

	out := captureStdout(func() {
		printFabricationAndEvidenceWarnings(resp, &evalResult)
	})

	if !strings.Contains(out, "FABRICATION RISK") {
		t.Errorf("output = %q, want FABRICATION RISK", out)
	}
	if !strings.Contains(out, "UNVERIFIED EVIDENCE (primary, non-blocking)") {
		t.Errorf("output = %q, want primary UNVERIFIED EVIDENCE line", out)
	}
	if strings.Contains(out, "unverified evidence (secondary, non-blocking)") {
		t.Errorf("output = %q, want no secondary line when UnverifiedEvidenceSecondary is empty", out)
	}
	if evalResult.UnverifiedEvidenceSecondary {
		t.Error("evalResult.UnverifiedEvidenceSecondary = true, want false")
	}
}

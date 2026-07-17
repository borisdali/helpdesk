package main

import (
	"context"
	"errors"
	"testing"

	"helpdesk/agentutil"
)

// mockCompleter returns a fixed response string for every call.
func mockCompleter(response string, err error) agentutil.TextCompleter {
	return func(_ context.Context, _ string) (string, error) {
		return response, err
	}
}

// panicCompleter fails the test if it is ever invoked.
func panicCompleter(t *testing.T) agentutil.TextCompleter {
	t.Helper()
	return func(_ context.Context, _ string) (string, error) {
		t.Fatal("completer must not be called in this case")
		return "", nil
	}
}

// ── classifyAttribution ───────────────────────────────────────────────────────

func TestClassifyAttribution_KnownClass(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	got := classifyAttribution(context.Background(),
		mockCompleter("connection-pool-saturation", nil),
		"The pool is exhausted, saturation is the root cause.",
		classes,
	)
	if got != "connection-pool-saturation" {
		t.Errorf("got %q, want connection-pool-saturation", got)
	}
}

func TestClassifyAttribution_KnownClass_CaseInsensitive(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	got := classifyAttribution(context.Background(),
		mockCompleter("Connection-Pool-Saturation", nil), // LLM returns wrong case
		"The pool is exhausted.",
		classes,
	)
	// EqualFold comparison → canonical casing returned.
	if got != "connection-pool-saturation" {
		t.Errorf("got %q, want connection-pool-saturation (canonical casing)", got)
	}
}

func TestClassifyAttribution_EmptyClasses(t *testing.T) {
	got := classifyAttribution(context.Background(),
		panicCompleter(t),
		"Some response text about pool saturation.",
		nil, // empty classes → early return, completer must not be called
	)
	if got != attributionUnknown {
		t.Errorf("got %q, want %q", got, attributionUnknown)
	}
}

func TestClassifyAttribution_EmptyResponseText(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := classifyAttribution(context.Background(),
		panicCompleter(t),
		"", // empty response → early return
		classes,
	)
	if got != attributionUnknown {
		t.Errorf("got %q, want %q", got, attributionUnknown)
	}
}

func TestClassifyAttribution_CompleterError(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := classifyAttribution(context.Background(),
		mockCompleter("", errors.New("api timeout")),
		"pool saturation confirmed",
		classes,
	)
	if got != attributionUnknown {
		t.Errorf("got %q, want %q on completer error", got, attributionUnknown)
	}
}

func TestClassifyAttribution_ClassNotInList(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	// LLM invents a class not in the allowed list.
	got := classifyAttribution(context.Background(),
		mockCompleter("invented-class-not-in-list", nil),
		"The database is slow.",
		classes,
	)
	if got != attributionUnknown {
		t.Errorf("got %q, want %q (invented class must be UNKNOWN)", got, attributionUnknown)
	}
}

func TestClassifyAttribution_ExplicitUnknown(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := classifyAttribution(context.Background(),
		mockCompleter("UNKNOWN", nil),
		"Not sure what's happening.",
		classes,
	)
	if got != attributionUnknown {
		t.Errorf("got %q, want %q", got, attributionUnknown)
	}
}

func TestClassifyAttribution_ExplicitUnknown_CaseInsensitive(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := classifyAttribution(context.Background(),
		mockCompleter("unknown", nil), // lowercase
		"Not sure.",
		classes,
	)
	if got != attributionUnknown {
		t.Errorf("got %q, want %q", got, attributionUnknown)
	}
}

func TestClassifyAttribution_MarkdownFence_ReturnsUnknown(t *testing.T) {
	// classifyAttribution does not strip markdown fences — the LLM should return
	// the raw class string. If it wraps output in a fence, we get UNKNOWN.
	classes := []string{"connection-pool-saturation"}
	got := classifyAttribution(context.Background(),
		mockCompleter("```\nconnection-pool-saturation\n```", nil),
		"The pool is saturated.",
		classes,
	)
	// Fenced output doesn't match any class after TrimSpace → UNKNOWN.
	if got != attributionUnknown {
		t.Errorf("got %q, want %q (markdown fence not stripped)", got, attributionUnknown)
	}
}

func TestClassifyAttribution_TrailingWhitespace_Stripped(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := classifyAttribution(context.Background(),
		mockCompleter("  connection-pool-saturation  \n", nil), // whitespace around output
		"The pool is saturated.",
		classes,
	)
	if got != "connection-pool-saturation" {
		t.Errorf("got %q, want connection-pool-saturation (TrimSpace should handle whitespace)", got)
	}
}

// ── computeAttributionSummary ─────────────────────────────────────────────────

func TestComputeAttributionSummary_Consistent(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	results := []EvalResult{
		{ResponseText: "pool is saturated"},
		{ResponseText: "pool is saturated again"},
		{ResponseText: "saturation confirmed"},
	}
	s := computeAttributionSummary(context.Background(),
		mockCompleter("connection-pool-saturation", nil),
		results, classes, "1.0",
	)
	if s.PrimaryAttribution != "connection-pool-saturation" {
		t.Errorf("PrimaryAttribution = %q, want connection-pool-saturation", s.PrimaryAttribution)
	}
	if !s.AttributionConsistent {
		t.Error("AttributionConsistent = false, want true")
	}
	if s.AttributionDistribution["connection-pool-saturation"] != 3 {
		t.Errorf("distribution[saturation] = %d, want 3", s.AttributionDistribution["connection-pool-saturation"])
	}
	if s.TaxonomyVersion != "1.0" {
		t.Errorf("TaxonomyVersion = %q, want 1.0", s.TaxonomyVersion)
	}
}

func TestComputeAttributionSummary_Split(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	callCount := 0
	// First two calls → saturation, third → leak.
	completer := agentutil.TextCompleter(func(_ context.Context, _ string) (string, error) {
		callCount++
		if callCount <= 2 {
			return "connection-pool-saturation", nil
		}
		return "connection-pool-leak", nil
	})
	results := []EvalResult{
		{ResponseText: "saturation"},
		{ResponseText: "saturation again"},
		{ResponseText: "leak"},
	}
	s := computeAttributionSummary(context.Background(), completer, results, classes, "1.0")

	if s.PrimaryAttribution != "connection-pool-saturation" {
		t.Errorf("PrimaryAttribution = %q, want connection-pool-saturation (plurality)", s.PrimaryAttribution)
	}
	if s.AttributionConsistent {
		t.Error("AttributionConsistent = true, want false (split)")
	}
	if s.AttributionDistribution["connection-pool-saturation"] != 2 {
		t.Errorf("distribution[saturation] = %d, want 2", s.AttributionDistribution["connection-pool-saturation"])
	}
	if s.AttributionDistribution["connection-pool-leak"] != 1 {
		t.Errorf("distribution[leak] = %d, want 1", s.AttributionDistribution["connection-pool-leak"])
	}
}

func TestComputeAttributionSummary_JudgeSpread_NonZero(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	results := []EvalResult{
		{ResponseText: "sat", DiagnosisScore: 0.8},
		{ResponseText: "sat", DiagnosisScore: 0.9},
		{ResponseText: "sat", DiagnosisScore: 1.0},
	}
	s := computeAttributionSummary(context.Background(),
		mockCompleter("connection-pool-saturation", nil),
		results, classes, "1.0",
	)
	if s.JudgeSpread <= 0 {
		t.Errorf("JudgeSpread = %v, want > 0 for varied diagnosis scores", s.JudgeSpread)
	}
}

func TestComputeAttributionSummary_JudgeSpread_ZeroWhenUniform(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	results := []EvalResult{
		{ResponseText: "sat", DiagnosisScore: 0.8},
		{ResponseText: "sat", DiagnosisScore: 0.8},
		{ResponseText: "sat", DiagnosisScore: 0.8},
	}
	s := computeAttributionSummary(context.Background(),
		mockCompleter("connection-pool-saturation", nil),
		results, classes, "1.0",
	)
	// Identical scores → std dev is effectively 0 (allow for floating-point noise).
	if s.JudgeSpread > 1e-10 {
		t.Errorf("JudgeSpread = %v, want ~0 for identical diagnosis scores", s.JudgeSpread)
	}
}

func TestComputeAttributionSummary_JudgeSpread_ComputedEvenWhenSplit(t *testing.T) {
	// JudgeSpread is always computed from DiagnosisScore, regardless of attribution consistency.
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	callCount := 0
	completer := agentutil.TextCompleter(func(_ context.Context, _ string) (string, error) {
		callCount++
		if callCount == 1 {
			return "connection-pool-saturation", nil
		}
		return "connection-pool-leak", nil
	})
	results := []EvalResult{
		{ResponseText: "saturation", DiagnosisScore: 0.8},
		{ResponseText: "leak", DiagnosisScore: 1.0},
	}
	s := computeAttributionSummary(context.Background(), completer, results, classes, "1.0")

	if s.AttributionConsistent {
		t.Error("expected split attribution (AttributionConsistent = false)")
	}
	// JudgeSpread is still computed — it's meaningful when analysing divergent runs.
	if s.JudgeSpread <= 0 {
		t.Errorf("JudgeSpread = %v, want > 0 (scores 0.8 and 1.0)", s.JudgeSpread)
	}
}

func TestComputeAttributionSummary_EmptyClasses(t *testing.T) {
	// When classes is nil, classifyAttribution returns UNKNOWN for every result
	// without calling the completer.
	results := []EvalResult{
		{ResponseText: "pool is saturated"},
		{ResponseText: "more saturation"},
	}
	s := computeAttributionSummary(context.Background(),
		panicCompleter(t), // must not be called
		results, nil, "1.0",
	)
	if s.PrimaryAttribution != attributionUnknown {
		t.Errorf("PrimaryAttribution = %q, want %q", s.PrimaryAttribution, attributionUnknown)
	}
	if s.AttributionConsistent {
		t.Error("AttributionConsistent = true, want false when all UNKNOWN")
	}
}

func TestComputeAttributionSummary_NilResults(t *testing.T) {
	s := computeAttributionSummary(context.Background(),
		panicCompleter(t),
		nil, []string{"connection-pool-saturation"}, "1.0",
	)
	if s.PrimaryAttribution != attributionUnknown {
		t.Errorf("PrimaryAttribution = %q, want %q for nil results", s.PrimaryAttribution, attributionUnknown)
	}
}

func TestComputeAttributionSummary_NilCompleter(t *testing.T) {
	results := []EvalResult{
		{ResponseText: "pool saturation"},
	}
	s := computeAttributionSummary(context.Background(),
		nil, // nil completer → early return
		results, []string{"connection-pool-saturation"}, "1.0",
	)
	if s.PrimaryAttribution != attributionUnknown {
		t.Errorf("PrimaryAttribution = %q, want %q for nil completer", s.PrimaryAttribution, attributionUnknown)
	}
}

func TestComputeAttributionSummary_TaxonomyVersionPropagated(t *testing.T) {
	results := []EvalResult{{ResponseText: "sat"}}
	s := computeAttributionSummary(context.Background(),
		mockCompleter("connection-pool-saturation", nil),
		results, []string{"connection-pool-saturation"}, "2.1",
	)
	if s.TaxonomyVersion != "2.1" {
		t.Errorf("TaxonomyVersion = %q, want 2.1", s.TaxonomyVersion)
	}
}

func TestComputeAttributionSummary_AllUnknown(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	results := []EvalResult{
		{ResponseText: "I cannot determine the cause"},
		{ResponseText: "unknown root cause"},
	}
	s := computeAttributionSummary(context.Background(),
		mockCompleter("UNKNOWN", nil), // LLM says UNKNOWN for all
		results, classes, "1.0",
	)
	if s.PrimaryAttribution != attributionUnknown {
		t.Errorf("PrimaryAttribution = %q, want %q when all UNKNOWN", s.PrimaryAttribution, attributionUnknown)
	}
	if s.AttributionConsistent {
		t.Error("AttributionConsistent = true, want false when all UNKNOWN")
	}
	if s.AttributionDistribution[attributionUnknown] != 2 {
		t.Errorf("distribution[UNKNOWN] = %d, want 2", s.AttributionDistribution[attributionUnknown])
	}
}

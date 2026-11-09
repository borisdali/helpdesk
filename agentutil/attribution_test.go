package agentutil

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockCompleter returns a fixed response string for every call.
func mockCompleter(response string, err error) TextCompleter {
	return func(_ context.Context, _ string) (string, error) {
		return response, err
	}
}

// panicCompleter fails the test if it is ever invoked.
func panicCompleter(t *testing.T) TextCompleter {
	t.Helper()
	return func(_ context.Context, _ string) (string, error) {
		t.Fatal("completer must not be called in this case")
		return "", nil
	}
}

func TestClassifyAttribution_KnownClass(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	got := ClassifyAttribution(context.Background(),
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
	got := ClassifyAttribution(context.Background(),
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
	got := ClassifyAttribution(context.Background(),
		panicCompleter(t),
		"Some response text about pool saturation.",
		nil, // empty classes → early return, completer must not be called
	)
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q", got, AttributionUnknown)
	}
}

func TestClassifyAttribution_EmptyResponseText(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := ClassifyAttribution(context.Background(),
		panicCompleter(t),
		"", // empty response → early return
		classes,
	)
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q", got, AttributionUnknown)
	}
}

func TestClassifyAttribution_CompleterError(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := ClassifyAttribution(context.Background(),
		mockCompleter("", errors.New("api timeout")),
		"pool saturation confirmed",
		classes,
	)
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q on completer error", got, AttributionUnknown)
	}
}

func TestClassifyAttribution_ClassNotInList(t *testing.T) {
	classes := []string{"connection-pool-saturation", "connection-pool-leak"}
	// LLM invents a class not in the allowed list.
	got := ClassifyAttribution(context.Background(),
		mockCompleter("invented-class-not-in-list", nil),
		"The database is slow.",
		classes,
	)
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q (invented class must be UNKNOWN)", got, AttributionUnknown)
	}
}

func TestClassifyAttribution_ExplicitUnknown(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := ClassifyAttribution(context.Background(),
		mockCompleter("UNKNOWN", nil),
		"Not sure what's happening.",
		classes,
	)
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q", got, AttributionUnknown)
	}
}

func TestClassifyAttribution_ExplicitUnknown_CaseInsensitive(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := ClassifyAttribution(context.Background(),
		mockCompleter("unknown", nil), // lowercase
		"Not sure.",
		classes,
	)
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q", got, AttributionUnknown)
	}
}

func TestClassifyAttribution_MarkdownFence_ReturnsUnknown(t *testing.T) {
	// ClassifyAttribution does not strip markdown fences — the LLM should return
	// the raw class string. If it wraps output in a fence, we get UNKNOWN.
	classes := []string{"connection-pool-saturation"}
	got := ClassifyAttribution(context.Background(),
		mockCompleter("```\nconnection-pool-saturation\n```", nil),
		"The pool is saturated.",
		classes,
	)
	// Fenced output doesn't match any class after TrimSpace → UNKNOWN.
	if got != AttributionUnknown {
		t.Errorf("got %q, want %q (markdown fence not stripped)", got, AttributionUnknown)
	}
}

// capturingCompleter records the prompt it was given and answers with a class.
func capturingCompleter(answer string, got *string) TextCompleter {
	return func(_ context.Context, prompt string) (string, error) {
		*got = prompt
		return answer, nil
	}
}

func TestClassifyAttributionDescribed_PutsDescriptionsInFrontOfTheClassifier(t *testing.T) {
	classes := []string{"plan-feasible", "plan-not-feasible", "plan-unverified"}
	descriptions := map[string]string{
		"plan-feasible":     "status=feasible.\n  Still feasible when the data loss exceeds the objective.",
		"plan-not-feasible": "status=not_feasible: no backup before the target.",
		// plan-unverified deliberately has none.
	}
	var prompt string
	got := ClassifyAttributionDescribed(context.Background(), capturingCompleter("plan-feasible", &prompt),
		"ROOT_CAUSE: the plan is feasible. ALERT: loss exceeds the RPO.", classes, descriptions)
	if got != "plan-feasible" {
		t.Errorf("got %q, want plan-feasible", got)
	}
	// A description follows its class, on one line however the YAML wrapped it.
	for _, want := range []string{
		"  - plan-feasible: status=feasible. Still feasible when the data loss exceeds the objective.\n",
		"  - plan-not-feasible: status=not_feasible: no backup before the target.\n",
		"  - plan-unverified\n",
		"  - UNKNOWN",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "plan-unverified:") {
		t.Errorf("a class with no description was given one:\n%s", prompt)
	}
}

// What a run had to warn about is not its root cause. A feasible plan that
// exceeds the recovery objective was once filed as not-feasible because the
// classifier let the alert move it.
func TestClassifyAttribution_TellsTheClassifierWarningsAreNotTheRootCause(t *testing.T) {
	var prompt string
	ClassifyAttribution(context.Background(), capturingCompleter("a", &prompt), "text", []string{"a", "b"})
	for _, want := range []string{"ROOT_CAUSE line", "objective verdicts", "not part of the root cause", "Return ONLY the category name"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
}

// A description is not a label: the classifier is told to answer with the
// name, and an answer that carries the description is not accepted.
func TestClassifyAttributionDescribed_AnAnswerWithTheDescriptionIsUnknown(t *testing.T) {
	got := ClassifyAttributionDescribed(context.Background(),
		mockCompleter("plan-feasible: status=feasible", nil), "text", []string{"plan-feasible"},
		map[string]string{"plan-feasible": "status=feasible"})
	if got != AttributionUnknown {
		t.Errorf("got %q, want UNKNOWN", got)
	}
}

func TestClassifyAttribution_TrailingWhitespace_Stripped(t *testing.T) {
	classes := []string{"connection-pool-saturation"}
	got := ClassifyAttribution(context.Background(),
		mockCompleter("  connection-pool-saturation  \n", nil), // whitespace around output
		"The pool is saturated.",
		classes,
	)
	if got != "connection-pool-saturation" {
		t.Errorf("got %q, want connection-pool-saturation (TrimSpace should handle whitespace)", got)
	}
}

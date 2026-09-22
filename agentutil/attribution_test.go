package agentutil

import (
	"context"
	"errors"
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

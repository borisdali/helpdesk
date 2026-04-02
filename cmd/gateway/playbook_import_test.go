package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/toolregistry"
)

// newImportGateway returns a minimal Gateway wired up for import handler tests.
// If llmFn is non-nil it is set as the plannerLLM; a minimal toolRegistry is always provided.
func newImportGateway(t *testing.T, llmFn func(ctx context.Context, prompt string) (string, error)) *Gateway {
	t.Helper()
	g := &Gateway{}
	entries := []toolregistry.ToolEntry{
		{Name: "get_vacuum_status", Agent: "database", ActionClass: "read", FleetEligible: true, Description: "Vacuum status"},
	}
	g.toolRegistry = toolregistry.New(entries)
	if llmFn != nil {
		g.plannerLLM = llmFn
	}
	return g
}

func doImportRequest(t *testing.T, g *Gateway, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/playbooks/import", bytes.NewReader(data))
	w := httptest.NewRecorder()
	g.handlePlaybookImport(w, req)
	return w
}

// --- format=yaml (no LLM) ---

func TestHandlePlaybookImport_YAMLFormat(t *testing.T) {
	g := newImportGateway(t, nil) // no LLM needed

	yamlText := `
series_id: pbs_test
name: Test Playbook
version: "1.0"
problem_class: performance
author: alice
description: Investigate slow queries on the primary.
symptoms:
  - slow queries observed
guidance: Check pg_stat_activity and explain plans.
escalation:
  - escalate if query > 30 minutes
target_hints: []
`
	w := doImportRequest(t, g, map[string]any{
		"text":   yamlText,
		"format": "yaml",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp PlaybookImportResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Draft == nil {
		t.Fatal("expected non-nil draft")
	}
	if resp.Draft.Name != "Test Playbook" {
		t.Errorf("name = %q, want Test Playbook", resp.Draft.Name)
	}
	if resp.Draft.Source != "imported" {
		t.Errorf("source = %q, want imported", resp.Draft.Source)
	}
	if resp.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0", resp.Confidence)
	}
}

func TestHandlePlaybookImport_InvalidYAML(t *testing.T) {
	g := newImportGateway(t, nil)

	w := doImportRequest(t, g, map[string]any{
		"text":   "not: valid: yaml: ::::",
		"format": "yaml",
	})

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

func TestHandlePlaybookImport_YAMLMissingDescription_LowConfidence(t *testing.T) {
	g := newImportGateway(t, nil)

	// Valid YAML but missing description → confidence drops to 0.8
	w := doImportRequest(t, g, map[string]any{
		"text":   "series_id: pbs_x\nname: X Playbook\n",
		"format": "yaml",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp PlaybookImportResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Confidence != 0.8 {
		t.Errorf("confidence = %v, want 0.8", resp.Confidence)
	}
	if len(resp.WarningMessages) == 0 {
		t.Error("expected at least one warning for missing description")
	}
}

// --- format=text / markdown (LLM path) ---

func mockLLMResponse(pb *audit.Playbook, warnings []string, confidence float64) string {
	wrapper := map[string]any{
		"playbook": map[string]any{
			"name":          pb.Name,
			"description":   pb.Description,
			"problem_class": pb.ProblemClass,
			"symptoms":      pb.Symptoms,
			"guidance":      pb.Guidance,
			"escalation":    pb.Escalation,
			"target_hints":  pb.TargetHints,
			"author":        pb.Author,
			"version":       pb.Version,
			"series_id":     pb.SeriesID,
		},
		"warning_messages": warnings,
		"confidence":       confidence,
	}
	data, _ := json.Marshal(wrapper)
	return string(data)
}

func TestHandlePlaybookImport_MarkdownViaLLM(t *testing.T) {
	want := &audit.Playbook{
		Name:         "Vacuum Runbook",
		Description:  "Run VACUUM on all tables.",
		ProblemClass: "capacity",
		Symptoms:     []string{"high dead tuples"},
		Guidance:     "Start with autovacuum settings.",
		Escalation:   []string{"DBA if bloat > 50%"},
	}

	llmFn := func(_ context.Context, prompt string) (string, error) {
		if !strings.Contains(prompt, "# My Vacuum Runbook") {
			t.Error("prompt does not include source text")
		}
		return mockLLMResponse(want, nil, 0.9), nil
	}
	g := newImportGateway(t, llmFn)

	w := doImportRequest(t, g, map[string]any{
		"text":   "# My Vacuum Runbook\n\nRun VACUUM on all tables regularly.",
		"format": "markdown",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp PlaybookImportResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Draft == nil {
		t.Fatal("expected non-nil draft")
	}
	if resp.Draft.Name != "Vacuum Runbook" {
		t.Errorf("name = %q, want Vacuum Runbook", resp.Draft.Name)
	}
	if resp.Draft.Source != "imported" {
		t.Errorf("source = %q, want imported", resp.Draft.Source)
	}
	if resp.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", resp.Confidence)
	}
}

func TestHandlePlaybookImport_LLMWarnings(t *testing.T) {
	llmFn := func(_ context.Context, _ string) (string, error) {
		pb := &audit.Playbook{Name: "Partial", Description: "Some description"}
		return mockLLMResponse(pb, []string{"author could not be extracted"}, 0.6), nil
	}
	g := newImportGateway(t, llmFn)

	w := doImportRequest(t, g, map[string]any{
		"text":   "some runbook content",
		"format": "text",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp PlaybookImportResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.WarningMessages) == 0 {
		t.Error("expected at least one warning message")
	}
}

func TestHandlePlaybookImport_LLMUnavailable(t *testing.T) {
	g := &Gateway{} // no LLM, no toolRegistry

	w := doImportRequest(t, g, map[string]any{
		"text":   "some runbook content",
		"format": "markdown",
	})

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandlePlaybookImport_EmptyText(t *testing.T) {
	g := newImportGateway(t, nil)

	w := doImportRequest(t, g, map[string]any{
		"text":   "   ",
		"format": "text",
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandlePlaybookImport_UnsupportedFormat(t *testing.T) {
	g := newImportGateway(t, nil)

	w := doImportRequest(t, g, map[string]any{
		"text":   "some content",
		"format": "pdf",
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandlePlaybookImport_HintsApplied(t *testing.T) {
	// LLM returns a draft with empty name; hint should fill it.
	llmFn := func(_ context.Context, _ string) (string, error) {
		pb := &audit.Playbook{Description: "Fix slow queries"}
		return mockLLMResponse(pb, nil, 0.7), nil
	}
	g := newImportGateway(t, llmFn)

	w := doImportRequest(t, g, map[string]any{
		"text":   "slow query runbook",
		"format": "text",
		"hints": map[string]any{
			"name":          "Slow Query Fix",
			"problem_class": "performance",
		},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp PlaybookImportResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Draft.Name != "Slow Query Fix" {
		t.Errorf("name = %q, want Slow Query Fix (from hint)", resp.Draft.Name)
	}
	if resp.Draft.ProblemClass != "performance" {
		t.Errorf("problem_class = %q, want performance (from hint)", resp.Draft.ProblemClass)
	}
}

// --- parseImportResponse (markdown fence stripping) ---

func TestParseImportResponse_StripMarkdownFences(t *testing.T) {
	pb := &audit.Playbook{
		Name:         "Fence Test",
		Description:  "LLM wrapped the JSON in fences",
		ProblemClass: "performance",
	}
	inner := mockLLMResponse(pb, nil, 0.85)

	// Simulate common LLM wrapping patterns.
	cases := []struct {
		name string
		raw  string
	}{
		{"json fence", "```json\n" + inner + "\n```"},
		{"plain fence", "```\n" + inner + "\n```"},
		{"fence with trailing newline", "```json\n" + inner + "\n```\n"},
		{"no fence (baseline)", inner},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings, confidence, err := parseImportResponse(tc.raw)
			if err != nil {
				t.Fatalf("parseImportResponse(%q): %v", tc.name, err)
			}
			if got.Name != "Fence Test" {
				t.Errorf("name = %q, want Fence Test", got.Name)
			}
			if got.Source != "imported" {
				t.Errorf("source = %q, want imported", got.Source)
			}
			if confidence != 0.85 {
				t.Errorf("confidence = %v, want 0.85", confidence)
			}
			_ = warnings
		})
	}
}

// --- assembleImportPrompt ---

func TestAssembleImportPrompt_IncludesToolCatalog(t *testing.T) {
	entries := []toolregistry.ToolEntry{
		{Name: "get_vacuum_status", Agent: "database", ActionClass: "read", FleetEligible: true, Description: "Vacuum status"},
	}
	r := toolregistry.New(entries)
	catalog := buildPlannerToolCatalog(r)

	prompt := assembleImportPrompt("some runbook text", "markdown", PlaybookImportHints{}, catalog)

	if !strings.Contains(prompt, "get_vacuum_status") {
		t.Error("prompt should include tool catalog entry")
	}
	if !strings.Contains(prompt, "some runbook text") {
		t.Error("prompt should include source text")
	}
	if !strings.Contains(prompt, "markdown") {
		t.Error("prompt should include format name")
	}
}

func TestAssembleImportPrompt_RundeckNote(t *testing.T) {
	prompt := assembleImportPrompt("job content", "rundeck", PlaybookImportHints{}, "")
	if !strings.Contains(prompt, "Rundeck") {
		t.Error("prompt should mention Rundeck for rundeck format")
	}
}

func TestAssembleImportPrompt_HintsSection(t *testing.T) {
	hints := PlaybookImportHints{Name: "My Playbook", ProblemClass: "performance"}
	prompt := assembleImportPrompt("text", "text", hints, "")

	if !strings.Contains(prompt, "My Playbook") {
		t.Error("prompt should include name hint")
	}
	if !strings.Contains(prompt, "performance") {
		t.Error("prompt should include problem_class hint")
	}
}

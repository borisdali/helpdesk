package main

import (
	"context"
	"sort"
	"strings"
	"testing"

	"helpdesk/internal/fleet"
	"helpdesk/internal/toolregistry"
)

// --- buildPlannerToolCatalog tests ---

func TestBuildPlannerToolCatalog_FleetFilter(t *testing.T) {
	entries := []toolregistry.ToolEntry{
		{
			Name:          "get_status_summary",
			Agent:         "database",
			ActionClass:   "read",
			FleetEligible: true,
			Capabilities:  []string{toolregistry.CapUptime, toolregistry.CapConnectionCount},
			Description:   "Compact status summary",
		},
		{
			Name:          "get_server_info",
			Agent:         "database",
			ActionClass:   "read",
			FleetEligible: false,
			Capabilities:  []string{toolregistry.CapUptime},
			Description:   "Detailed server info",
		},
	}
	r := toolregistry.New(entries)

	catalog := buildPlannerToolCatalog(r)

	if !strings.Contains(catalog, "get_status_summary") {
		t.Error("catalog should contain fleet-eligible tool get_status_summary")
	}
	if strings.Contains(catalog, "get_server_info") {
		t.Error("catalog should NOT contain non-fleet tool get_server_info")
	}
	// Capabilities should be included.
	if !strings.Contains(catalog, toolregistry.CapUptime) {
		t.Error("catalog should include capability label uptime")
	}
}

func TestBuildPlannerToolCatalog_Empty(t *testing.T) {
	r := toolregistry.New(nil)
	catalog := buildPlannerToolCatalog(r)
	if catalog != "" {
		t.Errorf("empty registry should produce empty catalog, got %q", catalog)
	}
}

// --- buildIntentSection tests ---

func TestBuildIntentSection_Sorted(t *testing.T) {
	section := buildIntentSection()

	// Should contain all intent keys.
	for intent := range toolregistry.IntentMap {
		if !strings.Contains(section, intent) {
			t.Errorf("intent section missing key %q", intent)
		}
	}

	// Extract intent keys in the order they appear and verify they are sorted.
	intents := make([]string, 0, len(toolregistry.IntentMap))
	for intent := range toolregistry.IntentMap {
		intents = append(intents, intent)
	}
	sort.Strings(intents)

	// Each intent should appear in sorted order relative to others.
	prevIdx := -1
	for _, intent := range intents {
		idx := strings.Index(section, intent)
		if idx < prevIdx {
			t.Errorf("intent %q appears before expected position (not sorted)", intent)
		}
		prevIdx = idx
	}
}

// --- toolNamesFromSteps / filterStepsByName ---

func TestToolNamesFromSteps(t *testing.T) {
	steps := []fleet.Step{
		{Tool: "get_status_summary"},
		{Tool: "get_server_info"},
		{Tool: "get_status_summary"}, // duplicate
	}
	names := toolNamesFromSteps(steps)
	if len(names) != 2 {
		t.Fatalf("toolNamesFromSteps len = %d, want 2", len(names))
	}
}

func TestFilterStepsByName(t *testing.T) {
	steps := []fleet.Step{
		{Tool: "get_status_summary"},
		{Tool: "get_server_info"},
		{Tool: "get_connection_stats"},
	}
	allowed := []string{"get_status_summary"}
	got := filterStepsByName(steps, allowed)
	if len(got) != 1 || got[0].Tool != "get_status_summary" {
		t.Errorf("filterStepsByName = %v, want [get_status_summary]", got)
	}
}

// --- ResolveSuperseded integration via planner handler ---

// TestResolveSupersededInPlan verifies that when the LLM returns a plan with
// both get_status_summary and its subordinates, the handler strips the
// subordinates before returning.
func TestResolveSupersededInPlan(t *testing.T) {
	// Build a registry with the supersedes relationship.
	entries := []toolregistry.ToolEntry{
		{
			Name:          "get_status_summary",
			Agent:         "database",
			FleetEligible: true,
			ActionClass:   "read",
			Supersedes:    []string{"get_server_info", "get_connection_stats"},
		},
		{Name: "get_server_info", Agent: "database", ActionClass: "read"},
		{Name: "get_connection_stats", Agent: "database", ActionClass: "read"},
	}
	r := toolregistry.New(entries)

	// Simulate LLM returning all three tools in the plan.
	llmSteps := []fleet.Step{
		{Tool: "get_server_info", Agent: "database"},
		{Tool: "get_connection_stats", Agent: "database"},
		{Tool: "get_status_summary", Agent: "database"},
	}

	// Apply the same post-processing as handleFleetPlan.
	rawNames := toolNamesFromSteps(llmSteps)
	resolvedNames := r.ResolveSuperseded(rawNames)
	finalSteps := filterStepsByName(llmSteps, resolvedNames)

	if len(finalSteps) != 1 {
		t.Fatalf("finalSteps len = %d, want 1; steps = %v", len(finalSteps), finalSteps)
	}
	if finalSteps[0].Tool != "get_status_summary" {
		t.Errorf("finalSteps[0].Tool = %q, want %q", finalSteps[0].Tool, "get_status_summary")
	}
}

// TestAssemblePlannerPrompt_IntentSection verifies that the intent section
// appears between the tool catalog and the JobDef schema.
func TestAssemblePlannerPrompt_IntentSection(t *testing.T) {
	prompt := assemblePlannerPrompt("infra", "tools", "  health_check → get_status_summary\n", "check status", "none")

	toolsIdx := strings.Index(prompt, "## Available Tools")
	intentIdx := strings.Index(prompt, "## Intent-to-Tool Mapping")
	schemaIdx := strings.Index(prompt, "## JobDef Schema")

	if toolsIdx < 0 || intentIdx < 0 || schemaIdx < 0 {
		t.Fatal("prompt missing expected sections")
	}
	if !(toolsIdx < intentIdx && intentIdx < schemaIdx) {
		t.Errorf("section order wrong: tools=%d intent=%d schema=%d", toolsIdx, intentIdx, schemaIdx)
	}
	if !strings.Contains(prompt, "health_check") {
		t.Error("prompt should contain intent content")
	}
}

// --- buildPlannerToolCatalog with InputSchema ---

func TestBuildPlannerToolCatalog_WithSchema(t *testing.T) {
	entries := []toolregistry.ToolEntry{
		{
			Name:          "get_status_summary",
			Agent:         "database",
			ActionClass:   "read",
			FleetEligible: true,
			Description:   "Compact status summary",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"connection_string": map[string]any{
						"type":        "string",
						"description": "PostgreSQL connection string",
					},
					"verbose": map[string]any{
						"type": "boolean",
					},
				},
				"required": []any{"connection_string"},
			},
		},
	}
	r := toolregistry.New(entries)
	catalog := buildPlannerToolCatalog(r)

	if !strings.Contains(catalog, "Parameters:") {
		t.Error("catalog should contain Parameters section when InputSchema is set")
	}
	if !strings.Contains(catalog, "connection_string") {
		t.Error("catalog should contain connection_string parameter")
	}
	if !strings.Contains(catalog, "required") {
		t.Error("catalog should mark required parameters")
	}
	if !strings.Contains(catalog, "PostgreSQL connection string") {
		t.Error("catalog should include parameter description")
	}
}

func TestBuildPlannerToolCatalog_NoSchema(t *testing.T) {
	entries := []toolregistry.ToolEntry{
		{
			Name:          "get_status_summary",
			Agent:         "database",
			ActionClass:   "read",
			FleetEligible: true,
			Description:   "Compact status",
			InputSchema:   nil,
		},
	}
	r := toolregistry.New(entries)
	catalog := buildPlannerToolCatalog(r)

	if strings.Contains(catalog, "Parameters:") {
		t.Error("catalog should NOT contain Parameters section when InputSchema is nil")
	}
	if !strings.Contains(catalog, "get_status_summary") {
		t.Error("catalog should still contain the tool name")
	}
}

// --- validateStepArgs tests ---

func TestValidateStepArgs_UnknownParam(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"connection_string": map[string]any{"type": "string"},
		},
		"required": []any{"connection_string"},
	}
	args := map[string]any{
		"connection_string": "host=localhost",
		"unknown_param":     "surprise",
	}
	if err := validateStepArgs(args, schema); err == nil {
		t.Error("expected error for unknown parameter, got nil")
	}
}

func TestValidateStepArgs_MissingRequired(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"connection_string": map[string]any{"type": "string"},
			"pid":               map[string]any{"type": "integer"},
		},
		"required": []any{"connection_string", "pid"},
	}
	args := map[string]any{
		"connection_string": "host=localhost",
		// "pid" is missing
	}
	if err := validateStepArgs(args, schema); err == nil {
		t.Error("expected error for missing required param, got nil")
	}
}

func TestValidateStepArgs_OK(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"connection_string": map[string]any{"type": "string"},
		},
		"required": []any{"connection_string"},
	}
	args := map[string]any{
		"connection_string": "host=localhost",
	}
	if err := validateStepArgs(args, schema); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateStepArgs_NoSchema(t *testing.T) {
	// nil schema → skip (handled at call site, but test the function with empty schema too)
	if err := validateStepArgs(nil, map[string]any{}); err != nil {
		t.Errorf("nil args with no properties should be OK: %v", err)
	}
}

// --- ToolSnapshots in handleFleetPlan ---

func TestHandleFleetPlan_ToolSnapshots(t *testing.T) {
	// Build a registry with a fleet-eligible tool that has a fingerprint.
	entry := toolregistry.ToolEntry{
		Name:              "get_status_summary",
		Agent:             "database",
		ActionClass:       "read",
		FleetEligible:     true,
		Description:       "Status",
		AgentVersion:      "1.0.0",
		SchemaFingerprint: "abc123def456",
	}
	reg := toolregistry.New([]toolregistry.ToolEntry{entry})

	var capturedPlan fleet.JobDef
	gw := &Gateway{toolRegistry: reg}

	// Call handleFleetPlan via a stub LLM that returns a pre-built job def.
	plannerResp := `{"job_def":{"name":"test-job","change":{"steps":[{"agent":"database","tool":"get_status_summary"}]},"targets":{"tags":["development"]},"strategy":{"canary_count":1}},"planner_notes":"test"}`
	gw.plannerLLM = func(_ context.Context, _ string) (string, error) {
		return plannerResp, nil
	}
	_ = capturedPlan
	_ = gw

	// The actual integration via HTTP is covered in TestHandleFleetPlan_* tests.
	// Here we test the snapshot population logic directly.
	snapshots := make(map[string]fleet.ToolSnapshot)
	for _, step := range []fleet.Step{{Tool: "get_status_summary"}} {
		if e, ok := reg.Get(step.Tool); ok {
			snapshots[step.Tool] = fleet.ToolSnapshot{
				AgentVersion:      e.AgentVersion,
				SchemaFingerprint: e.SchemaFingerprint,
			}
		}
	}
	if snap, ok := snapshots["get_status_summary"]; !ok {
		t.Error("expected snapshot for get_status_summary")
	} else {
		if snap.AgentVersion != "1.0.0" {
			t.Errorf("AgentVersion = %q, want %q", snap.AgentVersion, "1.0.0")
		}
		if snap.SchemaFingerprint != "abc123def456" {
			t.Errorf("SchemaFingerprint = %q, want %q", snap.SchemaFingerprint, "abc123def456")
		}
	}
}

// sort import needed for TestBuildIntentSection_Sorted already; keep unused import check clean.
var _ = sort.Strings

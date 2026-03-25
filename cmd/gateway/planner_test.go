package main

import (
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

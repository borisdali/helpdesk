package main

import (
	"testing"

	"helpdesk/internal/fleet"
)

// ── replanMode flag ───────────────────────────────────────────────────────────

func TestReplanMode_DefaultEmpty(t *testing.T) {
	var r replanMode
	if r.isSet() {
		t.Error("zero-value replanMode should not be set")
	}
}

func TestReplanMode_BoolFlag(t *testing.T) {
	// --replan without a value: flag package passes "true" via IsBoolFlag
	var r replanMode
	if err := r.Set("true"); err != nil {
		t.Fatalf("Set(true): %v", err)
	}
	if r.mode != "stop" {
		t.Errorf("mode = %q, want %q", r.mode, "stop")
	}
	if !r.isSet() {
		t.Error("isSet() should be true after Set")
	}
}

func TestReplanMode_ExplicitStop(t *testing.T) {
	var r replanMode
	if err := r.Set("stop"); err != nil {
		t.Fatalf("Set(stop): %v", err)
	}
	if r.mode != "stop" {
		t.Errorf("mode = %q, want %q", r.mode, "stop")
	}
}

func TestReplanMode_Auto(t *testing.T) {
	var r replanMode
	if err := r.Set("auto"); err != nil {
		t.Fatalf("Set(auto): %v", err)
	}
	if r.mode != "auto" {
		t.Errorf("mode = %q, want %q", r.mode, "auto")
	}
}

func TestReplanMode_Invalid(t *testing.T) {
	var r replanMode
	if err := r.Set("execute"); err == nil {
		t.Error("Set(execute) should return an error")
	}
}

// ── checkPlanDivergence ───────────────────────────────────────────────────────

func makeJobDef(tools ...string) *fleet.JobDef {
	steps := make([]fleet.Step, len(tools))
	for i, t := range tools {
		steps[i] = fleet.Step{Tool: t}
	}
	return &fleet.JobDef{Change: fleet.Change{Steps: steps}}
}

func TestCheckPlanDivergence_Identical(t *testing.T) {
	orig := makeJobDef("tool_a", "tool_b")
	fresh := makeJobDef("tool_a", "tool_b")
	d := checkPlanDivergence(orig, fresh)
	if d.Significant() {
		t.Errorf("identical plans should not be significant: %s", d)
	}
	if len(d.AddedTools) != 0 || len(d.RemovedTools) != 0 {
		t.Errorf("expected no added/removed tools, got %+v", d)
	}
}

func TestCheckPlanDivergence_SameToolsMoreSteps(t *testing.T) {
	// Same tool set but step count grew 50% — not significant (equal to threshold, not over)
	orig := makeJobDef("tool_a", "tool_a")  // 2 steps
	fresh := makeJobDef("tool_a", "tool_a", "tool_a") // 3 steps: delta=1, 1*2=2 == orig=2, not >
	d := checkPlanDivergence(orig, fresh)
	if d.Significant() {
		t.Errorf("50%% step growth should not trigger significant: %s", d)
	}
}

func TestCheckPlanDivergence_StepCountOver50Pct(t *testing.T) {
	orig := makeJobDef("tool_a", "tool_a")           // 2 steps
	fresh := makeJobDef("tool_a", "tool_a", "tool_a", "tool_a", "tool_a") // 5 steps: delta=3, 3*2=6 > 2
	d := checkPlanDivergence(orig, fresh)
	if !d.Significant() {
		t.Errorf("150%% step growth should be significant: %s", d)
	}
}

func TestCheckPlanDivergence_ToolAdded(t *testing.T) {
	orig := makeJobDef("tool_a")
	fresh := makeJobDef("tool_a", "tool_b")
	d := checkPlanDivergence(orig, fresh)
	if !d.Significant() {
		t.Error("added tool should be significant")
	}
	if len(d.AddedTools) != 1 || d.AddedTools[0] != "tool_b" {
		t.Errorf("AddedTools = %v, want [tool_b]", d.AddedTools)
	}
	if len(d.RemovedTools) != 0 {
		t.Errorf("unexpected RemovedTools: %v", d.RemovedTools)
	}
}

func TestCheckPlanDivergence_ToolRemoved(t *testing.T) {
	orig := makeJobDef("tool_a", "tool_b")
	fresh := makeJobDef("tool_a")
	d := checkPlanDivergence(orig, fresh)
	if !d.Significant() {
		t.Error("removed tool should be significant")
	}
	if len(d.RemovedTools) != 1 || d.RemovedTools[0] != "tool_b" {
		t.Errorf("RemovedTools = %v, want [tool_b]", d.RemovedTools)
	}
}

func TestCheckPlanDivergence_ToolReplaced(t *testing.T) {
	orig := makeJobDef("tool_a", "tool_b")
	fresh := makeJobDef("tool_a", "tool_c")
	d := checkPlanDivergence(orig, fresh)
	if !d.Significant() {
		t.Error("replaced tool should be significant")
	}
	if len(d.AddedTools) != 1 || len(d.RemovedTools) != 1 {
		t.Errorf("expected 1 added and 1 removed, got added=%v removed=%v", d.AddedTools, d.RemovedTools)
	}
}

func TestCheckPlanDivergence_DuplicateToolsNotCounted(t *testing.T) {
	// Same tool used multiple times in both — tool set is identical
	orig := makeJobDef("tool_a", "tool_a", "tool_a") // 3 steps
	fresh := makeJobDef("tool_a", "tool_a")           // 2 steps: delta=1, 1*2=2 == orig=3? no: 2 < 3
	d := checkPlanDivergence(orig, fresh)
	if len(d.AddedTools) != 0 || len(d.RemovedTools) != 0 {
		t.Errorf("same tool set, no adds/removes expected: %+v", d)
	}
	// Step count: delta=1, orig=3 → 1*2=2 < 3 → not significant
	if d.Significant() {
		t.Errorf("33%% step reduction should not be significant: %s", d)
	}
}

func TestCheckPlanDivergence_EmptyOriginal(t *testing.T) {
	orig := makeJobDef()
	fresh := makeJobDef("tool_a")
	d := checkPlanDivergence(orig, fresh)
	if !d.Significant() {
		t.Error("non-empty fresh vs empty original should be significant")
	}
}

func TestCheckPlanDivergence_BothEmpty(t *testing.T) {
	orig := makeJobDef()
	fresh := makeJobDef()
	d := checkPlanDivergence(orig, fresh)
	if d.Significant() {
		t.Errorf("both empty should not be significant: %s", d)
	}
}

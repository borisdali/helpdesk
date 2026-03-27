package main

import (
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/fleet"
)

func makeStep(agent, tool string) fleet.Step {
	return fleet.Step{Agent: agent, Tool: tool}
}

func makeStepWithArgs(agent, tool string, args map[string]any) fleet.Step {
	return fleet.Step{Agent: agent, Tool: tool, Args: args}
}

// TestBuildRollbackJobDef_StepsReversed verifies that the steps in the rollback
// job are the inverse operations in reverse order.
func TestBuildRollbackJobDef_StepsReversed(t *testing.T) {
	orig := &fleet.JobDef{
		Name: "deploy-v2",
		Change: fleet.Change{Steps: []fleet.Step{
			makeStep("database", "exec_update"),  // step 0
			makeStep("k8s", "scale_deployment"),  // step 1
		}},
		Strategy: fleet.Strategy{CanaryCount: 1},
	}

	servers := []string{"server-a", "server-b"}
	plans := map[string][]*audit.RollbackPlan{
		"server-a": {
			{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "database", Tool: "exec_sql", Args: map[string]any{"sql": "ROLLBACK UPDATE"}}},
			{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{"replicas": 3}}},
		},
		"server-b": {
			{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "database", Tool: "exec_sql", Args: map[string]any{"sql": "ROLLBACK UPDATE"}}},
			{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{"replicas": 3}}},
		},
	}

	rollback, err := BuildRollbackJobDef(orig, plans, "all", servers)
	if err != nil {
		t.Fatalf("BuildRollbackJobDef: %v", err)
	}

	steps := rollback.Change.Steps
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}

	// Step 0 of rollback should be the inverse of original step 1 (scale_deployment).
	if steps[0].Tool != "scale_deployment" {
		t.Errorf("step[0].Tool = %q, want scale_deployment (inverse of original step 1)", steps[0].Tool)
	}
	// Step 1 of rollback should be the inverse of original step 0 (exec_update).
	if steps[1].Tool != "exec_sql" {
		t.Errorf("step[1].Tool = %q, want exec_sql (inverse of original step 0)", steps[1].Tool)
	}
}

// TestBuildRollbackJobDef_CanaryLast verifies that the server that was the canary
// (first in original order) is moved to last in the rollback target list.
func TestBuildRollbackJobDef_CanaryLast(t *testing.T) {
	orig := &fleet.JobDef{
		Name:   "deploy",
		Change: fleet.Change{Steps: []fleet.Step{makeStep("k8s", "scale_deployment")}},
		Strategy: fleet.Strategy{CanaryCount: 1},
	}

	// canary is first: [canary, wave1, wave2]
	servers := []string{"canary", "wave1", "wave2"}
	plans := map[string][]*audit.RollbackPlan{
		"canary": {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
		"wave1":  {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
		"wave2":  {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
	}

	rollback, err := BuildRollbackJobDef(orig, plans, "all", servers)
	if err != nil {
		t.Fatalf("BuildRollbackJobDef: %v", err)
	}

	names := rollback.Targets.Names
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d: %v", len(names), names)
	}
	// canary should be last
	if names[len(names)-1] != "canary" {
		t.Errorf("last server = %q, want canary (canary-last rollback order)", names[len(names)-1])
	}
}

// TestBuildRollbackJobDef_NonReversibleStep annotates the step with a note instead
// of using the inverse op.
func TestBuildRollbackJobDef_NonReversibleStep(t *testing.T) {
	orig := &fleet.JobDef{
		Name:   "mixed",
		Change: fleet.Change{Steps: []fleet.Step{makeStep("k8s", "restart_deployment")}},
		Strategy: fleet.Strategy{CanaryCount: 0},
	}

	servers := []string{"s1"}
	plans := map[string][]*audit.RollbackPlan{
		"s1": {{Reversibility: audit.ReversibilityNo, InverseOp: nil}},
	}

	rollback, err := BuildRollbackJobDef(orig, plans, "all", servers)
	if err != nil {
		t.Fatalf("BuildRollbackJobDef: %v", err)
	}

	step := rollback.Change.Steps[0]
	// When there's no inverse op, the original step is kept with a note.
	if step.Tool != "restart_deployment" {
		t.Errorf("step.Tool = %q, want restart_deployment (kept as-is)", step.Tool)
	}
	if _, hasNote := step.Args["_rollback_note"]; !hasNote {
		t.Error("non-reversible step should have _rollback_note in args")
	}
}

// TestBuildRollbackJobDef_ScopeCanaryOnly selects only the first server.
func TestBuildRollbackJobDef_ScopeCanaryOnly(t *testing.T) {
	orig := &fleet.JobDef{
		Name:     "deploy",
		Change:   fleet.Change{Steps: []fleet.Step{makeStep("k8s", "scale_deployment")}},
		Strategy: fleet.Strategy{CanaryCount: 1},
	}

	servers := []string{"canary", "wave1", "wave2"}
	plans := map[string][]*audit.RollbackPlan{
		"canary": {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
		"wave1":  {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
		"wave2":  {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
	}

	rollback, err := BuildRollbackJobDef(orig, plans, "canary_only", servers)
	if err != nil {
		t.Fatalf("BuildRollbackJobDef: %v", err)
	}
	if len(rollback.Targets.Names) != 1 {
		t.Errorf("expected 1 server for canary_only, got %d", len(rollback.Targets.Names))
	}
	if rollback.Targets.Names[0] != "canary" {
		t.Errorf("server = %q, want canary", rollback.Targets.Names[0])
	}
}

// TestBuildRollbackJobDef_NameContainsRollback verifies the job name is prefixed.
func TestBuildRollbackJobDef_NameContainsRollback(t *testing.T) {
	orig := &fleet.JobDef{
		Name:   "deploy-v2",
		Change: fleet.Change{Steps: []fleet.Step{makeStep("k8s", "scale_deployment")}},
	}
	plans := map[string][]*audit.RollbackPlan{
		"s1": {{Reversibility: audit.ReversibilityYes, InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment", Args: map[string]any{}}}},
	}

	rollback, err := BuildRollbackJobDef(orig, plans, "all", []string{"s1"})
	if err != nil {
		t.Fatalf("BuildRollbackJobDef: %v", err)
	}
	if rollback.Name != "rollback: deploy-v2" {
		t.Errorf("Name = %q, want 'rollback: deploy-v2'", rollback.Name)
	}
}

// TestBuildRollbackJobDef_NilOriginalDef returns an error.
func TestBuildRollbackJobDef_NilOriginalDef(t *testing.T) {
	_, err := BuildRollbackJobDef(nil, nil, "all", nil)
	if err == nil {
		t.Error("expected error for nil original def, got nil")
	}
}

// TestBuildRollbackJobDef_NoSteps returns an error.
func TestBuildRollbackJobDef_NoSteps(t *testing.T) {
	orig := &fleet.JobDef{Name: "empty", Change: fleet.Change{}}
	_, err := BuildRollbackJobDef(orig, nil, "all", []string{"s1"})
	if err == nil {
		t.Error("expected error for job with no steps, got nil")
	}
}

// TestReverseCanaryOrder verifies the canary-last reordering.
func TestReverseCanaryOrder(t *testing.T) {
	tests := []struct {
		servers     []string
		canaryCount int
		wantLast    string
	}{
		{[]string{"canary", "w1", "w2"}, 1, "canary"},
		{[]string{"c1", "c2", "w1", "w2"}, 2, "c2"},
		{[]string{"only"}, 1, "only"},
		{[]string{"a", "b"}, 0, ""}, // no canary → stable sort
	}

	for _, tc := range tests {
		result := reverseCanaryOrder(tc.servers, tc.canaryCount)
		if tc.wantLast == "" {
			continue
		}
		if result[len(result)-1] != tc.wantLast {
			t.Errorf("reverseCanaryOrder(%v, %d) last=%q, want %q",
				tc.servers, tc.canaryCount, result[len(result)-1], tc.wantLast)
		}
	}
}

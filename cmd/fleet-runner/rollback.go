package main

import (
	"fmt"
	"sort"
	"strings"

	"helpdesk/internal/audit"
	"helpdesk/internal/fleet"
)

// BuildRollbackJobDef constructs a reverse fleet job from an original job definition
// and per-server rollback plans. The resulting job:
//   - Runs steps in reverse order (step N-1, N-2, ..., 0).
//   - Substitutes each step's args with the inverse operation from the corresponding plan.
//   - Applies the scope filter (which servers to roll back).
//   - Lists servers in reverse canary order (canary-last instead of canary-first).
//
// serverPlans maps each server name to the slice of RollbackPlan objects for that
// server's steps (in original step order; this function reverses them).
// scope is a JSON array of server names, "all", "canary_only", or "failed_only".
// serverOrder is the slice of server names in the order they were processed by
// the original job (canary server is first).
func BuildRollbackJobDef(
	originalDef *fleet.JobDef,
	serverPlans map[string][]*audit.RollbackPlan,
	scope string,
	serverOrder []string,
) (*fleet.JobDef, error) {
	if originalDef == nil {
		return nil, fmt.Errorf("original job definition is required")
	}
	if len(originalDef.Change.Steps) == 0 {
		return nil, fmt.Errorf("original job has no steps to reverse")
	}

	// Resolve the server list to roll back based on scope.
	servers, err := resolveRollbackScope(scope, serverOrder, serverPlans)
	if err != nil {
		return nil, fmt.Errorf("resolve scope: %w", err)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no servers selected by scope %q", scope)
	}

	// Build reversed steps. For each step (in reverse order), use the inverse
	// operation from the rollback plan if available; otherwise keep the original
	// step as a no-op marker so the operator knows it was not reversible.
	origSteps := originalDef.Change.Steps
	reverseSteps := make([]fleet.Step, 0, len(origSteps))

	for i := len(origSteps) - 1; i >= 0; i-- {
		origStep := origSteps[i]

		// Collect rollback plans for this step index across all servers.
		// Use the first available non-nil plan; if none, keep original step.
		var plan *audit.RollbackPlan
		for _, srv := range servers {
			if plans, ok := serverPlans[srv]; ok && i < len(plans) && plans[i] != nil {
				if plans[i].Reversibility == audit.ReversibilityYes && plans[i].InverseOp != nil {
					plan = plans[i]
					break
				}
			}
		}

		if plan != nil && plan.InverseOp != nil {
			reverseSteps = append(reverseSteps, fleet.Step{
				Agent:     plan.InverseOp.Agent,
				Tool:      plan.InverseOp.Tool,
				Args:      plan.InverseOp.Args,
				OnFailure: origStep.OnFailure,
			})
		} else {
			// No reversible plan for this step — include original step with a comment
			// in args so the operator can review what was skipped.
			annotatedArgs := make(map[string]any, len(origStep.Args)+1)
			for k, v := range origStep.Args {
				annotatedArgs[k] = v
			}
			annotatedArgs["_rollback_note"] = fmt.Sprintf(
				"step %d (%s/%s) has no inverse operation; review manually",
				i, origStep.Agent, origStep.Tool)
			reverseSteps = append(reverseSteps, fleet.Step{
				Agent:     origStep.Agent,
				Tool:      origStep.Tool,
				Args:      annotatedArgs,
				OnFailure: "stop",
			})
		}
	}

	// Targets: use explicit server names in canary-last order (reversed from original).
	// The canary server ran first; in rollback it should run last to validate
	// the rollback before applying it to the remaining fleet.
	rollbackTargets := fleet.Targets{
		Names: reverseCanaryOrder(servers, originalDef.Strategy.CanaryCount),
	}

	// Strategy: mirror the original but with canary at the end.
	// We achieve this by setting CanaryCount=0 (process all as waves) since
	// the names list already has canary at the end — the last server acts as
	// a "post-validation" step rather than a pre-validation canary.
	rollbackStrategy := fleet.Strategy{
		WaveSize:         originalDef.Strategy.WaveSize,
		WavePauseSeconds: originalDef.Strategy.WavePauseSeconds,
		FailureThreshold: originalDef.Strategy.FailureThreshold,
		CanaryCount:      0, // canary-last is achieved via server order, not strategy
	}

	rollbackDef := &fleet.JobDef{
		Name:   fmt.Sprintf("rollback: %s", originalDef.Name),
		Change: fleet.Change{Steps: reverseSteps},
		Targets:  rollbackTargets,
		Strategy: rollbackStrategy,
	}
	return rollbackDef, nil
}

// resolveRollbackScope filters the server list based on the scope string.
func resolveRollbackScope(scope string, serverOrder []string, serverPlans map[string][]*audit.RollbackPlan) ([]string, error) {
	scope = strings.TrimSpace(scope)
	switch scope {
	case "", "all":
		return serverOrder, nil
	case "canary_only":
		if len(serverOrder) == 0 {
			return nil, fmt.Errorf("server order is empty")
		}
		return serverOrder[:1], nil
	case "failed_only":
		// Select servers whose plans contain at least one reversible plan
		// (i.e., we have pre-state and can undo). The caller is responsible
		// for filtering "failed" servers using their fleet job server status.
		// Here we return all servers that have at least one reversible plan.
		var failed []string
		for _, srv := range serverOrder {
			plans := serverPlans[srv]
			for _, p := range plans {
				if p != nil && p.Reversibility == audit.ReversibilityYes {
					failed = append(failed, srv)
					break
				}
			}
		}
		return failed, nil
	default:
		// Treat as a JSON array of server names or a comma-separated list.
		if strings.HasPrefix(scope, "[") {
			// Simple JSON array parse (avoid importing encoding/json).
			scope = strings.TrimPrefix(scope, "[")
			scope = strings.TrimSuffix(scope, "]")
		}
		parts := strings.Split(scope, ",")
		allowSet := make(map[string]bool, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(strings.Trim(p, `" `))
			if p != "" {
				allowSet[p] = true
			}
		}
		var filtered []string
		for _, srv := range serverOrder {
			if allowSet[srv] {
				filtered = append(filtered, srv)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no servers matched scope list %q", scope)
		}
		return filtered, nil
	}
}

// reverseCanaryOrder reorders servers so that the first canaryCount servers
// (the original canaries) move to the end of the list. All other servers
// retain their relative order.
//
// Example: servers=[canary, wave1a, wave1b, wave2a], canaryCount=1
// Result:  [wave1a, wave1b, wave2a, canary]
func reverseCanaryOrder(servers []string, canaryCount int) []string {
	if canaryCount <= 0 || len(servers) <= canaryCount {
		result := make([]string, len(servers))
		copy(result, servers)
		sort.Strings(result) // deterministic order
		return result
	}
	canaries := servers[:canaryCount]
	rest := servers[canaryCount:]
	result := make([]string, 0, len(servers))
	result = append(result, rest...)
	result = append(result, canaries...)
	return result
}

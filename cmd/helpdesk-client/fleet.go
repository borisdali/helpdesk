package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"helpdesk/internal/client"
	"helpdesk/internal/fleet"
)

// fleetPlanResponse mirrors the gateway's FleetPlanResponse for JSON decoding.
type fleetPlanResponse struct {
	JobDef           fleet.JobDef `json:"job_def"`
	JobDefRaw        string       `json:"job_def_raw"`
	PlannerNotes     string       `json:"planner_notes"`
	RequiresApproval bool         `json:"requires_approval"`
	WrittenSteps     []string     `json:"written_steps,omitempty"`
	ExcludedServers  []string     `json:"excluded_servers,omitempty"`
	WarningMessages  []string     `json:"warning_messages,omitempty"`
}

// runFleetPlan sends a fleet plan request to the gateway and pretty-prints the result.
func runFleetPlan(ctx context.Context, cfg client.Config, description, targetHintsCSV string) error {
	c := client.New(cfg)

	// Build request body.
	reqBody := map[string]any{
		"description": description,
	}
	if targetHintsCSV != "" {
		hints := strings.Split(targetHintsCSV, ",")
		for i := range hints {
			hints[i] = strings.TrimSpace(hints[i])
		}
		reqBody["target_hints"] = hints
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.Do(ctx, "POST", "/api/v1/fleet/plan", bodyBytes)
	if err != nil {
		return fmt.Errorf("fleet plan request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		if e.Error != "" {
			return fmt.Errorf("gateway error (%d): %s", resp.StatusCode, e.Error)
		}
		return fmt.Errorf("gateway returned status %d", resp.StatusCode)
	}

	var plan fleetPlanResponse
	if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
		return fmt.Errorf("decode fleet plan response: %w", err)
	}

	printFleetPlan(plan)
	return nil
}

// printFleetPlan formats and prints a FleetPlanResponse to stdout.
func printFleetPlan(plan fleetPlanResponse) {
	fmt.Println("=== Fleet Job Plan ===")
	fmt.Println()
	fmt.Println("Planner notes:")
	fmt.Printf("  %s\n", plan.PlannerNotes)
	fmt.Println()

	if len(plan.ExcludedServers) > 0 {
		fmt.Printf("Excluded (sensitivity): %s\n", strings.Join(plan.ExcludedServers, ", "))
	}

	for _, warning := range plan.WarningMessages {
		fmt.Printf("WARNING: %s\n", warning)
	}

	if plan.RequiresApproval && len(plan.WrittenSteps) > 0 {
		fmt.Printf("APPROVAL REQUIRED for: %s\n", strings.Join(plan.WrittenSteps, ", "))
	}

	if len(plan.ExcludedServers) > 0 || len(plan.WarningMessages) > 0 || plan.RequiresApproval {
		fmt.Println()
	}

	fmt.Println("Generated job definition (save to a .json file and run with fleet-runner):")
	fmt.Println()

	// Use JobDefRaw if available (pre-formatted), otherwise marshal JobDef.
	if plan.JobDefRaw != "" {
		fmt.Println(plan.JobDefRaw)
	} else {
		pretty, err := json.MarshalIndent(plan.JobDef, "", "  ")
		if err != nil {
			fmt.Printf("(could not format job definition: %v)\n", err)
		} else {
			fmt.Println(string(pretty))
		}
	}

	fmt.Println()
	fmt.Printf("To submit: fleet-runner --job-file %s.json\n", plan.JobDef.Name)
}

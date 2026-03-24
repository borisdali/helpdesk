package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"helpdesk/internal/client"
	"helpdesk/internal/fleet"
)

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

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

	return printFleetPlan(plan)
}

// printFleetPlan formats and prints a FleetPlanResponse to stdout, then writes
// the job definition to <job-name>.json in the current directory.
func printFleetPlan(plan fleetPlanResponse) error {
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

	// Determine the JSON bytes to write.
	var jobJSON []byte
	if plan.JobDefRaw != "" {
		jobJSON = []byte(plan.JobDefRaw)
	} else {
		var err error
		jobJSON, err = json.MarshalIndent(plan.JobDef, "", "  ")
		if err != nil {
			return fmt.Errorf("format job definition: %w", err)
		}
	}

	// Write the job file so the user can run fleet-runner immediately.
	filename := slugify(plan.JobDef.Name) + ".json"
	if err := os.WriteFile(filename, append(jobJSON, '\n'), 0o644); err != nil {
		// Non-fatal: fall back to printing and letting the user save manually.
		fmt.Println("Generated job definition (save to a .json file and run with fleet-runner):")
		fmt.Println()
		fmt.Println(string(jobJSON))
		fmt.Println()
		fmt.Printf("(could not write %s: %v)\n", filename, err)
		fmt.Printf("To submit: fleet-runner --job-file %s\n", filename)
		return nil
	}

	fmt.Printf("Job file written: %s\n", filename)
	fmt.Println()
	fmt.Printf("To submit: fleet-runner --job-file %s\n", filename)
	return nil
}

// slugify converts a job name to a safe filename: lowercase, runs of
// non-alphanumeric characters replaced by a single hyphen, trimmed.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

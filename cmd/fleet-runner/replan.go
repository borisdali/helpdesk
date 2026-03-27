package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/fleet"
)

// replanMode is a custom flag.Value that accepts --replan (= "stop") or --replan=auto.
// IsBoolFlag makes Go's flag package accept --replan without a value, passing "true".
type replanMode struct{ mode string }

func (r *replanMode) IsBoolFlag() bool { return true }
func (r *replanMode) String() string   { return r.mode }
func (r *replanMode) Set(v string) error {
	switch v {
	case "true", "stop", "": // --replan without value
		r.mode = "stop"
	case "auto":
		r.mode = "auto"
	default:
		return fmt.Errorf("invalid replan mode %q: use --replan (write-and-stop) or --replan=auto (execute)", v)
	}
	return nil
}

// isSet reports whether --replan was provided at all.
func (r *replanMode) isSet() bool { return r.mode != "" }

// PlanDivergence describes how a replanned job differs from the original.
type PlanDivergence struct {
	OriginalSteps int
	FreshSteps    int
	AddedTools    []string
	RemovedTools  []string
}

func (d PlanDivergence) Significant() bool {
	if len(d.AddedTools) > 0 || len(d.RemovedTools) > 0 {
		return true
	}
	orig := d.OriginalSteps
	if orig == 0 {
		return d.FreshSteps > 0
	}
	delta := d.FreshSteps - orig
	if delta < 0 {
		delta = -delta
	}
	return delta*2 > orig // >50% change
}

func (d PlanDivergence) String() string {
	parts := []string{fmt.Sprintf("steps %d→%d", d.OriginalSteps, d.FreshSteps)}
	if len(d.AddedTools) > 0 {
		parts = append(parts, fmt.Sprintf("added tools: %s", strings.Join(d.AddedTools, ", ")))
	}
	if len(d.RemovedTools) > 0 {
		parts = append(parts, fmt.Sprintf("removed tools: %s", strings.Join(d.RemovedTools, ", ")))
	}
	return strings.Join(parts, "; ")
}

// checkPlanDivergence compares tool sets and step counts between original and fresh plans.
func checkPlanDivergence(original, fresh *fleet.JobDef) PlanDivergence {
	origTools := make(map[string]struct{})
	for _, s := range original.Change.Steps {
		origTools[s.Tool] = struct{}{}
	}
	freshTools := make(map[string]struct{})
	for _, s := range fresh.Change.Steps {
		freshTools[s.Tool] = struct{}{}
	}

	var added, removed []string
	for t := range freshTools {
		if _, ok := origTools[t]; !ok {
			added = append(added, t)
		}
	}
	for t := range origTools {
		if _, ok := freshTools[t]; !ok {
			removed = append(removed, t)
		}
	}
	return PlanDivergence{
		OriginalSteps: len(original.Change.Steps),
		FreshSteps:    len(fresh.Change.Steps),
		AddedTools:    added,
		RemovedTools:  removed,
	}
}

type fleetPlanResponse struct {
	JobDef fleet.JobDef `json:"job_def"`
}

// callFleetPlan posts a description + target hints to POST /api/v1/fleet/plan
// and returns the resulting JobDef. Used by --replan and --plan-description.
func callFleetPlan(gatewayURL, apiKey, description string, targetHints []string) (*fleet.JobDef, error) {
	type planReq struct {
		Description string   `json:"description"`
		TargetHints []string `json:"target_hints,omitempty"`
	}
	body, err := json.Marshal(planReq{Description: description, TargetHints: targetHints})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/plan"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Use http.Client without a Timeout so the context deadline governs.
	// gatewayPost sets its own 10s client timeout which would cut off LLM calls.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway plan call: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, string(respBody))
	}

	var planResp fleetPlanResponse
	if err := json.Unmarshal(respBody, &planResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &planResp.JobDef, nil
}

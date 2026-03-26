package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"helpdesk/internal/fleet"
)

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

	respBody, err := gatewayPost(ctx, url, apiKey, body)
	if err != nil {
		return nil, fmt.Errorf("gateway plan call: %w", err)
	}

	var resp fleetPlanResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &resp.JobDef, nil
}

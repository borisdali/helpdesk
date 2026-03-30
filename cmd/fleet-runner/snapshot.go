package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"helpdesk/internal/fleet"
)

type fleetSnapshotResponse struct {
	JobDef fleet.JobDef `json:"job_def"`
}

// callFleetSnapshot posts the job def to POST /api/v1/fleet/snapshot and returns
// the updated JobDef with refreshed tool_snapshots.
func callFleetSnapshot(gatewayURL, apiKey string, def *fleet.JobDef) (*fleet.JobDef, error) {
	type snapshotReq struct {
		JobDef fleet.JobDef `json:"job_def"`
	}
	body, err := json.Marshal(snapshotReq{JobDef: *def})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/snapshot"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	respBody, err := gatewayPost(ctx, url, apiKey, body)
	if err != nil {
		return nil, fmt.Errorf("gateway snapshot call: %w", err)
	}

	var resp fleetSnapshotResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &resp.JobDef, nil
}

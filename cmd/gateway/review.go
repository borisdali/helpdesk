package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"helpdesk/internal/fleet"
)

// FleetReviewRequest is the input to POST /api/v1/fleet/review.
type FleetReviewRequest struct {
	JobDef fleet.JobDef `json:"job_def"`
}

// FleetReviewIssue describes a single finding from the review.
type FleetReviewIssue struct {
	Severity string `json:"severity"` // "error", "warning", "info"
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// FleetReviewResponse is returned by POST /api/v1/fleet/review.
type FleetReviewResponse struct {
	// OK is true when no errors or warnings were found.
	OK     bool               `json:"ok"`
	Issues []FleetReviewIssue `json:"issues"`
	// SnapshotAge is the age of the oldest tool snapshot (for operator awareness).
	SnapshotAge string `json:"snapshot_age,omitempty"`
	// NewServers lists servers that exist in the current infrastructure but were
	// not present when the plan was created (PlanServers).
	NewServers []string `json:"new_servers,omitempty"`
}

// handleFleetReview handles POST /api/v1/fleet/review.
// It performs a pure registry / infrastructure analysis of a job def without
// calling an LLM or executing any steps. Useful for CI gates, approval flows,
// and scheduled-job health checks.
func (g *Gateway) handleFleetReview(w http.ResponseWriter, r *http.Request) {
	if g.toolRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet review requires tool registry")
		return
	}

	var req FleetReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	def := req.JobDef
	if len(def.Change.Steps) == 0 {
		writeError(w, http.StatusBadRequest, "job_def has no steps")
		return
	}

	var issues []FleetReviewIssue

	// 1. Validate all tool names exist in the registry.
	for _, step := range def.Change.Steps {
		if _, ok := g.toolRegistry.Get(step.Tool); !ok {
			issues = append(issues, FleetReviewIssue{
				Severity: "error",
				Code:     "unknown_tool",
				Message:  fmt.Sprintf("tool %q not found in registry", step.Tool),
			})
		}
	}

	// 2. Schema drift: compare stored snapshots against live registry.
	if len(def.ToolSnapshots) == 0 {
		issues = append(issues, FleetReviewIssue{
			Severity: "warning",
			Code:     "no_snapshots",
			Message:  "job_def has no tool_snapshots; schema drift cannot be checked (run --refresh-snapshots or replan)",
		})
	} else {
		liveEntries := g.toolRegistry.List()
		liveMap := make(map[string]struct {
			fingerprint string
			version     string
		}, len(liveEntries))
		for _, e := range liveEntries {
			liveMap[e.Name] = struct {
				fingerprint string
				version     string
			}{e.SchemaFingerprint, e.AgentVersion}
		}

		var oldestSnapshot time.Time
		for toolName, snap := range def.ToolSnapshots {
			if oldestSnapshot.IsZero() || snap.CapturedAt.Before(oldestSnapshot) {
				oldestSnapshot = snap.CapturedAt
			}
			live, exists := liveMap[toolName]
			if !exists {
				issues = append(issues, FleetReviewIssue{
					Severity: "warning",
					Code:     "tool_removed",
					Message:  fmt.Sprintf("tool %q was present at plan time but is no longer in the registry", toolName),
				})
				continue
			}
			if snap.SchemaFingerprint != "" && live.fingerprint != "" &&
				snap.SchemaFingerprint != live.fingerprint {
				issues = append(issues, FleetReviewIssue{
					Severity: "error",
					Code:     "schema_drift",
					Message: fmt.Sprintf(
						"tool %q schema changed: planned fingerprint=%s, current=%s (planned version=%s, current=%s, captured=%s)",
						toolName, snap.SchemaFingerprint, live.fingerprint,
						snap.AgentVersion, live.version,
						snap.CapturedAt.UTC().Format("2006-01-02T15:04:05Z")),
				})
			} else if snap.AgentVersion != "" && live.version != "" &&
				snap.AgentVersion != live.version {
				issues = append(issues, FleetReviewIssue{
					Severity: "warning",
					Code:     "version_changed",
					Message: fmt.Sprintf(
						"tool %q agent version changed: planned=%s, current=%s (fingerprint unchanged)",
						toolName, snap.AgentVersion, live.version),
				})
			}
		}
		if !oldestSnapshot.IsZero() {
			age := time.Since(oldestSnapshot)
			_ = age // reported via SnapshotAge below
		}
	}

	// 3. Restricted server check: does the job target any restricted servers?
	if g.infra != nil {
		_, restrictedServers := buildPlannerInfraContext(g.infra)
		restrictedSet := make(map[string]bool, len(restrictedServers))
		for _, s := range restrictedServers {
			restrictedSet[s] = true
		}
		resolved := resolveTargetsFromInfra(g.infra, def.Targets)
		for _, server := range resolved {
			if restrictedSet[server] {
				issues = append(issues, FleetReviewIssue{
					Severity: "warning",
					Code:     "restricted_server",
					Message:  fmt.Sprintf("job targets restricted server %q (sensitivity data); review carefully", server),
				})
			}
		}
	}

	// 4. New servers: servers that exist now but were absent at plan time.
	var newServers []string
	if g.infra != nil && len(def.PlanServers) > 0 {
		plannedSet := make(map[string]bool, len(def.PlanServers))
		for _, s := range def.PlanServers {
			plannedSet[s] = true
		}
		currentServers := resolveTargetsFromInfra(g.infra, def.Targets)
		for _, s := range currentServers {
			if !plannedSet[s] {
				newServers = append(newServers, s)
			}
		}
		sort.Strings(newServers)
		if len(newServers) > 0 {
			issues = append(issues, FleetReviewIssue{
				Severity: "info",
				Code:     "new_servers",
				Message: fmt.Sprintf(
					"%d server(s) added since plan: %v — they will be included in the next run",
					len(newServers), newServers),
			})
		}
	}

	// Compute oldest snapshot age string for the response.
	var snapshotAge string
	if len(def.ToolSnapshots) > 0 {
		var oldest time.Time
		for _, snap := range def.ToolSnapshots {
			if oldest.IsZero() || snap.CapturedAt.Before(oldest) {
				oldest = snap.CapturedAt
			}
		}
		if !oldest.IsZero() {
			d := time.Since(oldest).Round(time.Second)
			snapshotAge = d.String()
		}
	}

	// OK if no errors or warnings.
	ok := true
	for _, iss := range issues {
		if iss.Severity == "error" || iss.Severity == "warning" {
			ok = false
			break
		}
	}

	writeJSON(w, http.StatusOK, FleetReviewResponse{
		OK:          ok,
		Issues:      issues,
		SnapshotAge: snapshotAge,
		NewServers:  newServers,
	})
}

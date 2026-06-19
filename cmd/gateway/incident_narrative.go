package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// IncidentNarrative is the unified timeline view for a single triage incident:
// triage → operator gate → remediation, with evaluation scores and all feedback slots.
type IncidentNarrative struct {
	IncidentID  string                `json:"incident_id"`            // triage run_id
	StartedAt   time.Time             `json:"started_at"`
	ResolvedAt  *time.Time            `json:"resolved_at,omitempty"`
	DurationSec float64               `json:"duration_sec,omitempty"`
	Operator    string                `json:"operator"`
	Triage      TriageChapter         `json:"triage"`
	Gate        *GateChapter          `json:"gate,omitempty"`
	Remediation *RemediationChapter   `json:"remediation,omitempty"`
	// Feedback holds all operator feedback records for this incident (up to four:
	// triage/at_gate, triage/post_incident, remediation/at_gate, remediation/post_incident).
	Feedback    []audit.RunFeedback   `json:"feedback,omitempty"`
	// Evaluation holds automated faulttest eval scores for the triage run.
	Evaluation  *audit.RunEvaluation  `json:"evaluation,omitempty"`
}

// TriageChapter holds the investigative phase of the incident.
type TriageChapter struct {
	RunID            string                  `json:"run_id"`
	Playbook         string                  `json:"playbook"` // series_id
	Findings         string                  `json:"findings,omitempty"`
	DiagnosticReport *audit.DiagnosticReport `json:"diagnostic_report,omitempty"`
	Transcript       string                  `json:"transcript,omitempty"`
}

// GateChapter holds the operator approval decision.
type GateChapter struct {
	ApprovedBy   string    `json:"approved_by,omitempty"`
	AcknowledgedAt time.Time `json:"acknowledged_at,omitempty"`
	Resolution   string    `json:"resolution"` // "approved" | "denied"
	Reason       string    `json:"reason,omitempty"`
	ApprovalMode string    `json:"approval_mode,omitempty"`
}

// RemediationChapter holds the remediation playbook run.
type RemediationChapter struct {
	RunID      string                   `json:"run_id"`
	Playbook   string                   `json:"playbook"` // series_id
	Outcome    string                   `json:"outcome"`
	Steps      []*audit.PlaybookRunStep `json:"steps,omitempty"`
	Findings   string                   `json:"findings,omitempty"`
	Transcript string                   `json:"transcript,omitempty"`
}

// handleGetIncident handles GET /api/v1/incidents/{runID}.
// Assembles a unified IncidentNarrative from triage run, gate event,
// remediation run, and feedback.
func (g *Gateway) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "runID is required")
		return
	}

	// 1. Fetch the triage run.
	run, err := g.fetchPlaybookRun(r.Context(), runID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		slog.Error("handleGetIncident: failed to fetch triage run", "run_id", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch run")
		return
	}

	narrative := IncidentNarrative{
		IncidentID: runID,
		StartedAt:  run.StartedAt,
		Operator:   run.Operator,
		Triage: TriageChapter{
			RunID:            run.RunID,
			Playbook:         run.SeriesID,
			Findings:         run.FindingsSummary,
			DiagnosticReport: run.DiagnosticReport,
			Transcript:       run.AgentTranscript,
		},
	}

	// 2. Gate chapter — present when triage was an informed gate.
	isGated := run.Outcome == audit.OutcomeTransitioned ||
		run.Outcome == audit.OutcomeEscalated ||
		run.Outcome == audit.OutcomeAbandoned

	if isGated || run.Outcome == audit.OutcomeGatePending {
		gate := &GateChapter{}
		switch run.Outcome {
		case audit.OutcomeTransitioned, audit.OutcomeEscalated:
			gate.Resolution = "approved"
		case audit.OutcomeAbandoned:
			gate.Resolution = "denied"
		default:
			gate.Resolution = "pending"
		}
		// Enrich gate chapter from the gate_acknowledged audit event.
		if event := g.fetchGateAcknowledgedEvent(r.Context(), runID); event != nil {
			gate.AcknowledgedAt = event.Timestamp
			gate.Reason = ""
			if event.Output != nil {
				gate.Reason = event.Output.Response
			}
			// Extract resolvedBy from reasoning chain: "operator {X} acknowledged..."
			if len(event.Decision.ReasoningChain) > 0 {
				chain0 := event.Decision.ReasoningChain[0]
				if parts := strings.SplitN(chain0, " acknowledged", 2); len(parts) == 2 {
					gate.ApprovedBy = strings.TrimPrefix(parts[0], "operator ")
				}
			}
		}
		narrative.Gate = gate
	}

	// 3. Remediation run — the run that has this run as prior_run_id.
	if remRun := g.fetchRemediationRun(r.Context(), runID); remRun != nil {
		steps, _ := g.fetchRunSteps(r.Context(), remRun.RunID)
		narrative.Remediation = &RemediationChapter{
			RunID:      remRun.RunID,
			Playbook:   remRun.SeriesID,
			Outcome:    remRun.Outcome,
			Steps:      steps,
			Findings:   remRun.FindingsSummary,
			Transcript: remRun.AgentTranscript,
		}
		if !remRun.CompletedAt.IsZero() {
			t := remRun.CompletedAt
			narrative.ResolvedAt = &t
			narrative.DurationSec = t.Sub(run.StartedAt).Seconds()
		}
	} else if !run.CompletedAt.IsZero() && run.Outcome != audit.OutcomeGatePending {
		t := run.CompletedAt
		narrative.ResolvedAt = &t
		narrative.DurationSec = t.Sub(run.StartedAt).Seconds()
	}

	// 4. Feedback — all operator feedback slots for this incident.
	narrative.Feedback = g.fetchAllRunFeedback(r.Context(), runID)

	// 5. Evaluation — automated faulttest eval scores.
	narrative.Evaluation = g.fetchRunEvaluation(r.Context(), runID)

	writeJSON(w, http.StatusOK, narrative)
}

// fetchGateAcknowledgedEvent fetches the gate_acknowledged audit event for runID.
func (g *Gateway) fetchGateAcknowledgedEvent(ctx context.Context, runID string) *audit.Event {
	if g.auditURL == "" {
		return nil
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/events?trace_id=" + runID + "&event_type=gate_acknowledged&limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
	var events []audit.Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil || len(events) == 0 {
		return nil
	}
	return &events[0]
}

// fetchRemediationRun finds the remediation run that followed a triage run.
func (g *Gateway) fetchRemediationRun(ctx context.Context, triageRunID string) *audit.PlaybookRun {
	if g.auditURL == "" {
		return nil
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs?prior_run_id=" + triageRunID + "&limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var result struct {
		Runs []*audit.PlaybookRun `json:"runs"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Runs) == 0 {
		return nil
	}
	return result.Runs[0]
}

// fetchAllRunFeedback fetches all operator feedback records for a run (up to four:
// triage/at_gate, triage/post_incident, remediation/at_gate, remediation/post_incident).
func (g *Gateway) fetchAllRunFeedback(ctx context.Context, runID string) []audit.RunFeedback {
	if g.auditURL == "" {
		return nil
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/feedback"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
	var envelope struct {
		Feedback []audit.RunFeedback `json:"feedback"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil
	}
	return envelope.Feedback
}

// fetchRunEvaluation fetches automated eval scores for a run. Returns nil when none recorded.
func (g *Gateway) fetchRunEvaluation(ctx context.Context, runID string) *audit.RunEvaluation {
	if g.auditURL == "" {
		return nil
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs/" + runID + "/evaluation"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
	var ev audit.RunEvaluation
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil || ev.RunID == "" {
		return nil
	}
	return &ev
}


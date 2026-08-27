package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	IncidentID     string        `json:"incident_id"` // triage run_id
	StartedAt      time.Time     `json:"started_at"`
	ResolvedAt     *time.Time    `json:"resolved_at,omitempty"`
	DurationSec    float64       `json:"duration_sec,omitempty"`
	Operator       string        `json:"operator"`
	TriggerContext string        `json:"trigger_context,omitempty"` // original alert text that initiated the run
	Triage         TriageChapter `json:"triage"`
	Gate           *GateChapter  `json:"gate,omitempty"`
	// Escalations holds every intermediate hop reached via an explicit
	// ESCALATE_TO signal — further diagnosis, possibly on a different agent —
	// strictly between the triage chapter and the (optional) terminal
	// Remediation chapter. Most incidents have zero entries here.
	Escalations []EscalationHop `json:"escalations,omitempty"`
	// Remediation is populated only when the chain reached an explicit
	// TRANSITION_TO signal — it is not simply "whatever run followed triage."
	// A successor run reached via ESCALATE_TO is an escalation, not a
	// remediation, and appears in Escalations instead.
	Remediation *RemediationChapter `json:"remediation,omitempty"`
	// Feedback holds all operator feedback records for this incident (up to four:
	// triage/at_gate, triage/post_incident, remediation/at_gate, remediation/post_incident).
	Feedback []audit.RunFeedback `json:"feedback,omitempty"`
	// Evaluation holds automated faulttest eval scores for the triage run.
	Evaluation *audit.RunEvaluation `json:"evaluation,omitempty"`
	// Journeys links each phase of this incident to its audit Journey trace.
	// Triage = the WHY (reasoning chain, hypothesis building).
	// Remediation = the WHAT (tool calls, approvals, blast-radius decisions).
	Journeys []audit.IncidentJourneyRef `json:"journeys,omitempty"`
}

// TriageChapter holds the investigative phase of the incident.
type TriageChapter struct {
	RunID            string                  `json:"run_id"`
	Playbook         string                  `json:"playbook"` // series_id
	Findings         string                  `json:"findings,omitempty"`
	DiagnosticReport *audit.DiagnosticReport `json:"diagnostic_report,omitempty"`
	Transcript       string                  `json:"transcript,omitempty"`
	// TraceID identifies this chapter's Journey — the WHAT view behind this
	// chapter's WHY. Used to fetch HasMismatch/HasTargetDrift/HasProtocolViolation below.
	TraceID string `json:"trace_id,omitempty"`
	// HasMismatch/HasTargetDrift/HasProtocolViolation mirror the corresponding
	// Journey's flags (GET /v1/journeys?trace_id=X) — surfaced inline here so a
	// reader doesn't have to separately look up the Journey to know this
	// chapter's tool calls weren't fully verified. Absent (false) can mean
	// either "verified clean" or "no Journey data exists for this trace"
	// (fail-open by design, same ambiguity already present at the Journey
	// layer) — not a positive attestation either way.
	HasMismatch          bool `json:"has_mismatch,omitempty"`
	HasTargetDrift       bool `json:"has_target_drift,omitempty"`
	HasProtocolViolation bool `json:"has_protocol_violation,omitempty"`
	// SawSignalLine is read directly off the persisted PlaybookRun (not a
	// Journey lookup like the three flags above) — true iff the agent's raw
	// response had a TRANSITION_TO:/ESCALATE_TO: line at all, regardless of
	// its resolved value. Lets a reader distinguish "explicitly declined"
	// from a genuine protocol violation without needing the raw transcript.
	SawSignalLine bool `json:"saw_signal_line,omitempty"`
}

// GateChapter holds the operator approval decision.
type GateChapter struct {
	ApprovedBy     string    `json:"approved_by,omitempty"`
	AcknowledgedAt time.Time `json:"acknowledged_at,omitempty"`
	Resolution     string    `json:"resolution"` // "approved" | "denied"
	Reason         string    `json:"reason,omitempty"`
	ApprovalMode   string    `json:"approval_mode,omitempty"`
}

// RemediationChapter holds the remediation playbook run.
type RemediationChapter struct {
	RunID      string                   `json:"run_id"`
	Playbook   string                   `json:"playbook"` // series_id
	Outcome    string                   `json:"outcome"`
	Steps      []*audit.PlaybookRunStep `json:"steps,omitempty"`
	Findings   string                   `json:"findings,omitempty"`
	Transcript string                   `json:"transcript,omitempty"`
	// TraceID/HasMismatch/HasTargetDrift/HasProtocolViolation — see TriageChapter's doc comment.
	TraceID              string `json:"trace_id,omitempty"`
	HasMismatch          bool   `json:"has_mismatch,omitempty"`
	HasTargetDrift       bool   `json:"has_target_drift,omitempty"`
	HasProtocolViolation bool   `json:"has_protocol_violation,omitempty"`
	// SawSignalLine — see TriageChapter's doc comment.
	SawSignalLine bool `json:"saw_signal_line,omitempty"`
}

// EscalationHop is one intermediate playbook run reached via ESCALATE_TO,
// strictly between the triage chapter and the (optional) terminal
// remediation chapter. Most incidents have zero entries here — they only
// appear when an agent can't reach a diagnosis and hands off to another
// agent (e.g. a database agent escalating to a sysadmin agent).
type EscalationHop struct {
	RunID            string                   `json:"run_id"`
	Playbook         string                   `json:"playbook"` // series_id
	Outcome          string                   `json:"outcome"`
	EscalatedTo      string                   `json:"escalated_to,omitempty"`
	Findings         string                   `json:"findings,omitempty"`
	DiagnosticReport *audit.DiagnosticReport  `json:"diagnostic_report,omitempty"`
	Steps            []*audit.PlaybookRunStep `json:"steps,omitempty"`
	Transcript       string                   `json:"transcript,omitempty"`
	TraceID          string                   `json:"trace_id,omitempty"`
	StartedAt        time.Time                `json:"started_at"`
	CompletedAt      *time.Time               `json:"completed_at,omitempty"`
	// HasMismatch/HasTargetDrift/HasProtocolViolation — see TriageChapter's doc comment.
	HasMismatch          bool `json:"has_mismatch,omitempty"`
	HasTargetDrift       bool `json:"has_target_drift,omitempty"`
	HasProtocolViolation bool `json:"has_protocol_violation,omitempty"`
	// SawSignalLine — see TriageChapter's doc comment.
	SawSignalLine bool `json:"saw_signal_line,omitempty"`
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

	// hops is fetched here, ahead of the classification loop below, so each
	// chapter's own hopVerificationFlags window can be bounded by the NEXT
	// hop's StartedAt rather than this hop's own CompletedAt. CompletedAt is
	// too fragile a boundary: it can be recorded with a timestamp at or
	// before some event that still genuinely belongs to this same hop (e.g.
	// clock/goroutine-scheduling variance), which would wrongly exclude that
	// event — found via a real test failure after first shipping a
	// CompletedAt-bounded version of this fix. Hops run strictly
	// sequentially within one auto-chain, so "the next hop hadn't started
	// yet" is a race-free boundary that doesn't depend on exactly when
	// CompletedAt gets stamped. The last hop in the chain (whichever chapter
	// it becomes) has no next hop, so its window stays unbounded, same as
	// before.
	hops := g.fetchEscalationHops(r.Context(), runID)
	var triageWindowEnd time.Time
	if len(hops) > 0 {
		triageWindowEnd = hops[0].StartedAt
	}

	// eventsCache dedupes FetchDelegationVerificationEvents calls when two
	// chapters share a trace_id (e.g. an agent that ran triage and remediation
	// in one session — buildJourneyRefs already merges this case for the
	// Journeys[] list; this cache prevents fetching the same trace's events
	// twice for the flags below). Unlike a whole-trace Journey lookup, the raw
	// events are further narrowed per-chapter by hopVerificationFlags below —
	// necessary because force-mode auto-chaining can put multiple hops under
	// one shared trace_id (chainEscalation) when the caller supplies its own
	// X-Trace-ID, and a whole-trace aggregate can't distinguish between them
	// (found live via the real 3-hop DB→sysadmin→K8s chain: a later hop's
	// genuine mismatch was leaking backward onto an earlier, actually-clean
	// hop's reported HasMismatch).
	eventsCache := make(map[string][]audit.Event)
	lookupTraceEvents := func(traceID string) []audit.Event {
		if traceID == "" {
			return nil
		}
		if events, ok := eventsCache[traceID]; ok {
			return events
		}
		events := audit.FetchDelegationVerificationEvents(g.auditURL, g.auditAPIKey, traceID, run.StartedAt)
		eventsCache[traceID] = events
		return events
	}

	narrative := IncidentNarrative{
		IncidentID:     runID,
		StartedAt:      run.StartedAt,
		Operator:       run.Operator,
		TriggerContext: run.TriggerContext,
		Triage: TriageChapter{
			RunID:            run.RunID,
			Playbook:         run.SeriesID,
			Findings:         run.FindingsSummary,
			DiagnosticReport: run.DiagnosticReport,
			Transcript:       run.AgentTranscript,
			TraceID:          run.TraceID,
			SawSignalLine:    run.SawSignalLine,
		},
	}
	narrative.Triage.HasMismatch, narrative.Triage.HasTargetDrift, narrative.Triage.HasProtocolViolation =
		hopVerificationFlags(lookupTraceEvents(run.TraceID), run.StartedAt, triageWindowEnd)

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

	// 3. Escalation hops — every run that followed the triage run, walked to
	// completion (not just one hop). Each hop is classified by the signal
	// its PREDECESSOR emitted, not by chain position: a hop reached because
	// the predecessor's EscalatedTo was set is another diagnosis hop
	// (Escalations); a hop reached because the predecessor's TransitionedTo
	// was set is the remediation — singular, wherever it falls in the chain.
	// Classification stops at the first remediation hop found; a remediation
	// run that itself further escalates is out of scope for now.
	//
	// Each hop also gets a lookupTraceEvents call for HasMismatch/HasTargetDrift.
	// This is sequential, matching fetchRunSteps just below it and the rest of
	// this file/package's convention (no goroutine fan-out anywhere in
	// cmd/gateway) — worst case (maxEscalationHops = 20) this roughly doubles
	// the existing per-hop round-trip count, the same risk class as the
	// fetchRunSteps calls already here, not a new one.
	// hops was already fetched above (needed early for triageWindowEnd).

	var classified []*audit.PlaybookRun
	predecessor := run
	for i, hop := range hops {
		classified = append(classified, hop)
		var hopWindowEnd time.Time
		if i+1 < len(hops) {
			hopWindowEnd = hops[i+1].StartedAt
		}
		if predecessor.TransitionedTo != "" {
			steps, _ := g.fetchRunSteps(r.Context(), hop.RunID)
			rem := &RemediationChapter{
				RunID:         hop.RunID,
				Playbook:      hop.SeriesID,
				Outcome:       hop.Outcome,
				Steps:         steps,
				Findings:      hop.FindingsSummary,
				Transcript:    hop.AgentTranscript,
				TraceID:       hop.TraceID,
				SawSignalLine: hop.SawSignalLine,
			}
			rem.HasMismatch, rem.HasTargetDrift, rem.HasProtocolViolation =
				hopVerificationFlags(lookupTraceEvents(hop.TraceID), hop.StartedAt, hopWindowEnd)
			narrative.Remediation = rem
			predecessor = hop
			break
		}

		// Escalation hop: predecessor.EscalatedTo != "", or neither signal
		// set (defensive default — never guess a hop into Remediation
		// without an explicit TRANSITION_TO).
		hopSteps, _ := g.fetchRunSteps(r.Context(), hop.RunID)
		eh := EscalationHop{
			RunID:            hop.RunID,
			Playbook:         hop.SeriesID,
			Outcome:          hop.Outcome,
			EscalatedTo:      hop.EscalatedTo,
			Findings:         hop.FindingsSummary,
			DiagnosticReport: hop.DiagnosticReport,
			Steps:            hopSteps,
			Transcript:       hop.AgentTranscript,
			TraceID:          hop.TraceID,
			StartedAt:        hop.StartedAt,
			SawSignalLine:    hop.SawSignalLine,
		}
		eh.HasMismatch, eh.HasTargetDrift, eh.HasProtocolViolation =
			hopVerificationFlags(lookupTraceEvents(hop.TraceID), hop.StartedAt, hopWindowEnd)
		if !hop.CompletedAt.IsZero() {
			t := hop.CompletedAt
			eh.CompletedAt = &t
		}
		narrative.Escalations = append(narrative.Escalations, eh)
		predecessor = hop
	}

	if len(classified) > 0 {
		terminal := classified[len(classified)-1]
		if !terminal.CompletedAt.IsZero() {
			t := terminal.CompletedAt
			narrative.ResolvedAt = &t
			narrative.DurationSec = t.Sub(run.StartedAt).Seconds()
		}
	} else if !run.CompletedAt.IsZero() && run.Outcome != audit.OutcomeGatePending {
		t := run.CompletedAt
		narrative.ResolvedAt = &t
		narrative.DurationSec = t.Sub(run.StartedAt).Seconds()
	}
	narrative.Journeys = buildJourneyRefs(run, classified)

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

// hopVerificationFlags computes HasMismatch/HasTargetDrift/HasProtocolViolation for
// one hop by filtering the trace's delegation_verification events to those recorded
// within this hop's own [start, end) window — end exclusive when non-zero, unbounded
// when zero (still-open/most-recent hop). Needed because force-mode auto-chaining
// can put multiple hops under one shared trace_id (chainEscalation) when the caller
// supplies its own X-Trace-ID — a whole-trace aggregate can't distinguish between
// them; found live via the real 3-hop DB→sysadmin→K8s chain, where a later hop's
// genuine mismatch was leaking backward onto an earlier, actually-clean hop.
func hopVerificationFlags(events []audit.Event, start, end time.Time) (hasMismatch, hasTargetDrift, hasProtocolViolation bool) {
	for _, ev := range events {
		dv := ev.DelegationVerification
		if dv == nil || ev.Timestamp.Before(start) {
			continue
		}
		if !end.IsZero() && !ev.Timestamp.Before(end) {
			continue
		}
		hasMismatch = hasMismatch || dv.Mismatch
		hasTargetDrift = hasTargetDrift || len(dv.TargetDrift) > 0
		hasProtocolViolation = hasProtocolViolation || dv.ProtocolViolation
	}
	return
}

// maxEscalationHops bounds how many prior_run_id hops handleGetIncident will
// walk when assembling an incident narrative. This is a read-path
// runaway/cycle guard only — it is intentionally independent of, and larger
// than, playbooks.go's maxChainDepth (5), which is a live auto-chaining
// *policy* enforced during a single POST /playbooks/{id}/run request. This
// endpoint must be able to fully replay any historical incident even if that
// policy value changes in the future, so the two constants are deliberately
// unrelated.
const maxEscalationHops = 20

// fetchNextHop finds the run whose prior_run_id equals runID — i.e. the run
// that runID escalated or transitioned into — or nil if none exists (yet).
// This is the single-hop primitive fetchEscalationHops uses to walk forward.
func (g *Gateway) fetchNextHop(ctx context.Context, runID string) *audit.PlaybookRun {
	if g.auditURL == "" {
		return nil
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbook-runs?prior_run_id=" + runID + "&limit=1"
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

// fetchEscalationHops walks the prior_run_id chain starting from triageRunID,
// returning every subsequent PlaybookRun in chronological order: hops[0] is
// the immediate escalation from triage, hops[len(hops)-1] is the last run
// found. Returns an empty slice when the triage run has no follow-on runs.
// Stops early (logging a warning) on a detected cycle or once
// maxEscalationHops is reached, returning whatever was collected so far
// rather than failing the whole request.
func (g *Gateway) fetchEscalationHops(ctx context.Context, triageRunID string) []*audit.PlaybookRun {
	var hops []*audit.PlaybookRun
	seen := map[string]bool{triageRunID: true}
	cursor := triageRunID
	for i := 0; i < maxEscalationHops; i++ {
		next := g.fetchNextHop(ctx, cursor)
		if next == nil {
			return hops
		}
		if seen[next.RunID] {
			slog.Warn("fetchEscalationHops: cycle detected, stopping walk",
				"triage_run_id", triageRunID, "repeated_run_id", next.RunID)
			return hops
		}
		seen[next.RunID] = true
		hops = append(hops, next)
		cursor = next.RunID
	}
	slog.Warn("fetchEscalationHops: hit maxEscalationHops without reaching a terminal run",
		"triage_run_id", triageRunID, "max_hops", maxEscalationHops)
	return hops
}

// buildJourneyRefs assembles one audit.IncidentJourneyRef per phase of the
// incident: triage, any intermediate escalation hops, and the terminal
// remediation hop (if the chain reached one). Intermediate hops are labeled
// "escalation:1", "escalation:2", etc.; the hop produced by an explicit
// TRANSITION_TO is labeled "remediation" regardless of its position in the
// chain. Adjacent phases that share a non-empty trace_id (the agent handled
// both in a single session) are merged into one "phaseA+phaseB" entry,
// generalizing the historical "triage+remediation" merge to N-hop chains.
func buildJourneyRefs(run *audit.PlaybookRun, hops []*audit.PlaybookRun) []audit.IncidentJourneyRef {
	type labeled struct{ phase, traceID string }
	all := []labeled{{phase: "triage", traceID: run.TraceID}}

	predecessor := run
	escalationNum := 0
	for _, hop := range hops {
		phase := "remediation"
		if predecessor.TransitionedTo == "" {
			escalationNum++
			phase = fmt.Sprintf("escalation:%d", escalationNum)
		}
		all = append(all, labeled{phase: phase, traceID: hop.TraceID})
		predecessor = hop
	}

	var refs []audit.IncidentJourneyRef
	for i := 0; i < len(all); i++ {
		if all[i].traceID == "" {
			continue
		}
		phase, trace := all[i].phase, all[i].traceID
		for i+1 < len(all) && all[i+1].traceID == trace {
			phase += "+" + all[i+1].phase
			i++
		}
		refs = append(refs, audit.IncidentJourneyRef{Phase: phase, TraceID: trace})
	}
	return refs
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

package audit

// recovery_decision: the record of a recovery decision, as a typed event.
//
// A restore is a decision made in stages by different parties: the agent plans
// it (what would be lost, against which objectives), a person approves it at
// the gate (and acknowledges what they accept), and the restore tool decides
// again just before it stops anything (re-measuring, enforcing). Each stage
// leaves a recovery_decision event. Together they answer, long after the fact
// and without parsing tool-output text: what was required of this database,
// what was known, what was offered, who accepted what, and what was done.
//
// The objectives are recorded as a snapshot of what was in force, with where
// each value came from and a digest of the configuration it was read from. The
// configuration itself is not kept, so a later change to infrastructure.json
// does not rewrite what applied to a decision already made.

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// EventTypeRecoveryDecision is emitted at each stage of a recovery decision.
const EventTypeRecoveryDecision EventType = "recovery_decision"

// Stages of a recovery decision.
const (
	// RecoveryStagePlan: a plan was made (and shown to whoever asked for it).
	RecoveryStagePlan = "plan"
	// RecoveryStageGate: a person approved or denied the gate.
	RecoveryStageGate = "gate"
	// RecoveryStageExecution: the restore tool decided, just before it stops
	// anything: proceed or refuse.
	RecoveryStageExecution = "execution"
	// RecoveryStageOutcome: the remediation hop that carried out the recovery
	// finished. Its Decision is the run's outcome (resolved, failed, ...) and
	// DurationSeconds how long the hop took.
	RecoveryStageOutcome = "outcome"
)

// What a stage decided.
const (
	RecoveryDecisionPlanned   = "planned"
	RecoveryDecisionApproved  = "approved"
	RecoveryDecisionDenied    = "denied"
	RecoveryDecisionProceeded = "proceeded"
	RecoveryDecisionRefused   = "refused"
)

// RecoveryDecision is one stage of a recovery decision.
type RecoveryDecision struct {
	Stage    string `json:"stage"`
	Decision string `json:"decision"`
	// Reason says why, for a refusal or a denial.
	Reason string `json:"reason,omitempty"`
	// DecidedBy is the person who made the decision, for the gate stage:
	// whoever approved or denied it. Empty for the stages a tool decides.
	DecidedBy string `json:"decided_by,omitempty"`
	// DurationSeconds is, for the outcome stage, how long the remediation hop
	// ran: from its start, which is after the gate approval, to its completion.
	// That covers the restore, the restart and the verification, and not the
	// time spent waiting for a person. It is the recovery duration, not the
	// downtime: the instance may have been down before the hop began, and the
	// hop includes the agent's own reasoning time.
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
	// Tool is the tool the stage belongs to (plan_point_in_time_recovery,
	// restore_to_point_in_time); empty for the gate.
	Tool string `json:"tool,omitempty"`
	// Target is the database, as the config names it.
	Target string `json:"target,omitempty"`
	// RunID is the playbook run the gate belongs to (the gate stage).
	RunID string `json:"run_id,omitempty"`
	// RecoveryTarget is where the restore stops, in the form a run stores it
	// ("time=2026-10-28T14:05:00Z").
	RecoveryTarget string `json:"recovery_target,omitempty"`
	PlanDigest     string `json:"plan_digest,omitempty"`
	// PlanStatus is feasible, not_feasible or unverified.
	PlanStatus string `json:"plan_status,omitempty"`
	// RestoreBytes is, for the execution stage, the size of the cluster in the
	// backup the restore used (pgBackRest's own figure for it): what the restore
	// writes back, and so what its duration mostly depends on. It is kept so that
	// restore times can later be compared across a server's growth. Absent when
	// pgBackRest did not report a size, and on a record from before v0.35.
	RestoreBytes *uint64 `json:"restore_bytes,omitempty"`
	// Objectives is what was required of this database when the stage ran.
	Objectives *ObjectivesInForce `json:"objectives,omitempty"`
	// Options are the courses of action the stage had in front of it, each
	// with what it would cost and how it stands against the objectives. Today
	// there is one (a point-in-time restore); the record is shaped for several.
	Options []DecisionOption `json:"options,omitempty"`
	// Approval is what the approval carried (the gate and execution stages).
	Approval *ApprovalFacts `json:"approval,omitempty"`
}

// ObjectivesInForce is a snapshot of the resolved objectives and policy for one
// database, each with the level it came from (database, environment:<name>,
// default).
type ObjectivesInForce struct {
	Environment string `json:"environment,omitempty"`

	RPOSeconds *int64 `json:"rpo_seconds,omitempty"`
	RPOSource  string `json:"rpo_source,omitempty"`
	RTOSeconds *int64 `json:"rto_seconds,omitempty"`
	RTOSource  string `json:"rto_source,omitempty"`

	AvailabilitySLO    *float64 `json:"availability_slo,omitempty"`
	AvailabilitySource string   `json:"availability_source,omitempty"`
	// The window the SLO was read over and the downtime budget that gives, as
	// they were when the decision was made. The window source is a config level,
	// or "built-in default" when no level set one. Absent on events recorded
	// before v0.35 and when no budget could be computed.
	AvailabilityWindowSeconds *int64 `json:"availability_window_seconds,omitempty"`
	AvailabilityWindowSource  string `json:"availability_window_source,omitempty"`
	AvailabilityBudgetSeconds *int64 `json:"availability_budget_seconds,omitempty"`

	RecoveryPreference string `json:"recovery_preference,omitempty"`
	PreferenceSource   string `json:"preference_source,omitempty"`

	// Enforcement is the effective enforcement (warn when none is set, and
	// acknowledge when it could not be trusted); EnforcementSource where the
	// configured one came from, empty when it was not configured.
	Enforcement       string `json:"enforcement,omitempty"`
	EnforcementSource string `json:"enforcement_source,omitempty"`
	// AvailabilityEnforcement is the effective enforcement of the availability
	// budget (warn when none is set, acknowledge when it could not be trusted),
	// and its source where one was configured. Absent before v0.35.
	AvailabilityEnforcement       string `json:"availability_enforcement,omitempty"`
	AvailabilityEnforcementSource string `json:"availability_enforcement_source,omitempty"`

	// Problems are the configuration problems that affected the resolution.
	Problems []string `json:"problems,omitempty"`
	// ConfigDigest identifies the infrastructure config these were read from.
	ConfigDigest string `json:"config_digest,omitempty"`
}

// DecisionOption is one course of action and how it stands.
type DecisionOption struct {
	Name string `json:"name"`
	// Chosen marks the option the stage went ahead with (execution stage).
	Chosen bool `json:"chosen,omitempty"`

	// The measured loss. LossBasis is live_instance or archive_end;
	// LossUpperBound says the figures are at most that much.
	LossBasis      string   `json:"loss_basis,omitempty"`
	LossSeconds    *int64   `json:"loss_seconds,omitempty"`
	LossBytes      *uint64  `json:"loss_bytes,omitempty"`
	LossUpperBound bool     `json:"loss_upper_bound,omitempty"`
	LossAsOf       string   `json:"loss_as_of,omitempty"`
	LossUnmeasured []string `json:"loss_unmeasured,omitempty"`
	// LossAtLeastSeconds is a lower bound on the history discarded, for a target
	// whose exact time span is not known (an LSN), and LossAtLeastBasis what it
	// rests on. A breach of the rpo proven by it is recorded as the verdict.
	LossAtLeastSeconds *int64 `json:"loss_at_least_seconds,omitempty"`
	LossAtLeastBasis   string `json:"loss_at_least_basis,omitempty"`
	// The transactions the restore would discard, counted from the WAL with
	// pg_waldump. LossCommitsStatus is measured or unavailable (absent when no
	// attempt was made, and on a record from before v0.35). LossCommits is the
	// count, LossCommitsAtLeast says it is a lower bound, and LossCommitsReason
	// says why there is none.
	LossCommitsStatus  string `json:"loss_commits_status,omitempty"`
	LossCommits        *int   `json:"loss_commits,omitempty"`
	LossCommitsAtLeast bool   `json:"loss_commits_at_least,omitempty"`
	LossCommitsReason  string `json:"loss_commits_reason,omitempty"`

	// Verdict is the loss set against the rpo (within, exceeds, may_exceed,
	// not_comparable, no_objective, objective_invalid); Gate what approving
	// demands because of it ("", acknowledge, refuse).
	Verdict       string `json:"verdict,omitempty"`
	VerdictDetail string `json:"verdict_detail,omitempty"`
	Gate          string `json:"gate,omitempty"`

	// The restore set against the availability budget (the downtime the SLO
	// allows in its window): what is left of it, as the restore stood to use it.
	// BudgetVerdict is within, exceeds, may_exceed, not_comparable, no_objective,
	// objective_invalid or ledger_unavailable; BudgetGate what approving demands
	// because of it. BudgetRemainingSeconds is negative when the budget is
	// overspent. Absent on a record from before v0.35.
	BudgetVerdict          string `json:"budget_verdict,omitempty"`
	BudgetVerdictDetail    string `json:"budget_verdict_detail,omitempty"`
	BudgetGate             string `json:"budget_gate,omitempty"`
	BudgetRemainingSeconds *int64 `json:"budget_remaining_seconds,omitempty"`
	// How long the budget verdict took the restore to last, and on what:
	// "history_p90" (the p90 of this server's past restores, from ten of them),
	// "history_longest" (the longest of three to nine, too few for a p90) or
	// "declared_rto" (an upper bound). Absent when it was not compared. Records
	// from before this naming say "history", which meant the p90.
	BudgetDurationSeconds *int64 `json:"budget_duration_seconds,omitempty"`
	BudgetDurationBasis   string `json:"budget_duration_basis,omitempty"`

	// What this server's past restores have taken, from real incidents, as the
	// plan or the restore saw it. EstimateStatus is estimated, insufficient_history,
	// no_history or unavailable; the figures are present only when estimated.
	// EstimateP90Seconds only from ten samples (below that it would be the longest
	// under another name), EstimateMaxSeconds always, when estimated.
	// EstimateVsRTO is "within" or "exceeds" (some past restore took longer than the
	// declared rto), with EstimateOverRTO how many.
	EstimateStatus     string `json:"estimate_status,omitempty"`
	EstimateSamples    *int   `json:"estimate_samples,omitempty"`
	EstimateP50Seconds *int64 `json:"estimate_p50_seconds,omitempty"`
	EstimateP90Seconds *int64 `json:"estimate_p90_seconds,omitempty"`
	EstimateMaxSeconds *int64 `json:"estimate_max_seconds,omitempty"`
	EstimateVsRTO      string `json:"estimate_vs_rto,omitempty"`
	EstimateOverRTO    *int   `json:"estimate_over_rto,omitempty"`
}

// The objectives a restore can breach, as named in ApprovalFacts.Breaches.
const (
	BreachRPO                = "rpo"
	BreachAvailabilityBudget = "availability_budget"
)

// ApprovalFacts is what an approval carried.
type ApprovalFacts struct {
	ApprovedBy           string `json:"approved_by,omitempty"`
	AcknowledgedDataLoss bool   `json:"acknowledged_data_loss,omitempty"`
	AllowedStopServing   bool   `json:"allowed_stop_serving,omitempty"`
	AcknowledgedBreach   bool   `json:"acknowledged_breach,omitempty"`
	// Breaches are the objectives that stood in the way of this restore (each one
	// demanded the acknowledgement), so that a person who acknowledged an rpo
	// breach is not taken to have acknowledged a budget breach too. One
	// acknowledgement covers every breach shown at the gate; this says which
	// there were. Empty when nothing demanded it.
	Breaches []string `json:"breaches,omitempty"`
}

var (
	// password=<value> in a key=value connection string (or a URL query): a
	// bare word, or one in single or double quotes.
	// A quoted value may contain a backslash-escaped quote (libpq) or a doubled
	// one (SQL style); both are consumed so no part of the password is left.
	passwordKVRe = regexp.MustCompile(`(?i)(\bpassword\s*=\s*)('(?:[^'\\]|\\.|'')*'|"(?:[^"\\]|\\.|"")*"|[^\s&]+)`)
	// user:<password>@ in a URL's authority.
	urlUserinfoRe = regexp.MustCompile(`(?i)(://[^:/@\s]+:)[^@\s]+(@)`)
)

// RedactConnString masks the password in a connection string, in either the
// key=value form or the URL form, wherever it appears in s. Text without one
// comes back unchanged. A run's stored connection string carries the real
// password, and an audit record is read by far more people than a database
// is.
func RedactConnString(s string) string {
	if s == "" {
		return s
	}
	s = passwordKVRe.ReplaceAllString(s, "${1}***")
	return urlUserinfoRe.ReplaceAllString(s, "${1}***${2}")
}

// NewRecoveryDecisionEvent builds the audit event for one stage. traceID is the
// trace the event belongs to: the request's for the agent's stages, the run's
// for the gate, as gate_acknowledged does.
//
// The target and the reason are redacted here, for every emitter: a target can
// be a connection string (the run's own, or one a caller passed as the
// target), and a reason can quote one.
func NewRecoveryDecisionEvent(traceID, sessionID string, d RecoveryDecision) *Event {
	d.Target = RedactConnString(d.Target)
	d.Reason = RedactConnString(d.Reason)
	return &Event{
		EventID:          "rdc_" + uuid.New().String()[:8],
		Timestamp:        time.Now().UTC(),
		EventType:        EventTypeRecoveryDecision,
		TraceID:          traceID,
		Session:          Session{ID: sessionID},
		RecoveryDecision: &d,
	}
}

// RecordRecoveryDecision records one stage of a recovery decision. It does
// nothing, and returns nil, when auditing is off or the stage is unnamed.
//
// A failed write is logged at error level and returned. The decision itself
// does not depend on its record being writable, as for every other tool call
// (audit is best-effort at runtime; governed modes require it to be configured),
// but the caller is told, so that it can say in its own result that this
// decision is not in the audit trail.
func (ta *ToolAuditor) RecordRecoveryDecision(ctx context.Context, d RecoveryDecision) error {
	if ta == nil || ta.auditor == nil || d.Stage == "" {
		return nil
	}
	event := NewRecoveryDecisionEvent(ta.getTraceID(), ta.sessionID, d)
	if err := ta.auditor.Record(ctx, event); err != nil {
		slog.Error("recovery decision NOT recorded in the audit trail",
			"stage", d.Stage, "decision", d.Decision, "tool", d.Tool, "err", err)
		return err
	}
	return nil
}

// FetchRecoveryDecisionEvents returns the recovery_decision events of a trace
// recorded at or after since, oldest first as auditd returns them. Fails open:
// an unreachable auditd reads as no events.
func FetchRecoveryDecisionEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchEventsByType(auditURL, apiKey, traceID, string(EventTypeRecoveryDecision), since, true)
}

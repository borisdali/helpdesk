package main

// The recovery_decision events of a point-in-time restore (internal/audit's
// recovery_decision.go): what the agent records at the plan and at the moment
// it decides, just before the stop, to go ahead or to refuse. The gateway
// records the third stage, the gate. Everything is written from the same
// values the tool acted on, not reconstructed from text afterwards.

import (
	"context"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

const pitrOptionName = "point_in_time_restore"

// snapshotOf is the audit form of a database's resolved objectives: what was
// in force, where each value came from, and the digest of the config read.
func snapshotOf(res infra.ResolvedObjectives, configDigest string) *audit.ObjectivesInForce {
	s := &audit.ObjectivesInForce{
		Environment:  res.Environment,
		Problems:     res.Problems,
		ConfigDigest: configDigest,
		Enforcement:  res.EffectiveEnforcement(),
	}
	if res.RPO.Set {
		n := res.RPO.Seconds()
		s.RPOSeconds, s.RPOSource = &n, res.RPO.Source
	}
	if res.RTO.Set {
		n := res.RTO.Seconds()
		s.RTOSeconds, s.RTOSource = &n, res.RTO.Source
	}
	if res.AvailabilitySLO.Set {
		v := res.AvailabilitySLO.Value
		s.AvailabilitySLO, s.AvailabilitySource = &v, res.AvailabilitySLO.Source
	}
	// The window and budget the SLO gave, as they stood: the record must show the
	// numbers the approver saw, including a window that was only the built-in
	// default, not leave them to be recomputed under a later rule.
	if b := res.Budget(); b.Computed {
		w, a := int64(b.Window/time.Second), int64(b.Allowed.Round(time.Second)/time.Second)
		s.AvailabilityWindowSeconds, s.AvailabilityWindowSource, s.AvailabilityBudgetSeconds = &w, b.WindowSource, &a
	}
	if res.RecoveryPreference.Set {
		s.RecoveryPreference, s.PreferenceSource = res.RecoveryPreference.Value, res.RecoveryPreference.Source
	}
	if res.Enforcement.Set {
		s.EnforcementSource = res.Enforcement.Source
	}
	s.AvailabilityEnforcement = res.EffectiveAvailabilityEnforcement()
	if res.AvailabilityEnforcement.Set {
		s.AvailabilityEnforcementSource = res.AvailabilityEnforcement.Source
	}
	return s
}

// objectivesSnapshot is the snapshot for the database a call targets, or nil
// when infrastructure config is not loaded or names no such database. The
// lookup and the read both happen under the config lock.
func objectivesSnapshot(target string) *audit.ObjectivesInForce {
	infraConfigMu.RLock()
	defer infraConfigMu.RUnlock()
	if infraConfig == nil {
		return nil
	}
	key, _, err := lookupServer(infraConfig, target)
	if err != nil {
		return nil
	}
	res, ok := infraConfig.ResolveObjectives(key)
	if !ok {
		return nil
	}
	return snapshotOf(res, infraConfig.Digest)
}

// pitrOption is the audit form of the point-in-time restore as an option, with
// what it would cost and how that stands against the objective.
func pitrOption(est *DataLossEstimate, budget *availability.BudgetVerdict, chosen bool) audit.DecisionOption {
	o := audit.DecisionOption{Name: pitrOptionName, Chosen: chosen}
	if budget != nil {
		o.BudgetVerdict, o.BudgetVerdictDetail, o.BudgetGate, o.BudgetRemainingSeconds = budget.Status, budget.Detail, budget.Gate, budget.RemainingSeconds
	}
	if est == nil {
		return o
	}
	o.LossBasis, o.LossSeconds, o.LossBytes = est.Basis, est.WindowSeconds, est.WALBytes
	o.LossUpperBound, o.LossAsOf, o.LossUnmeasured = est.UpperBound, est.AsOf, est.Unmeasured
	o.LossAtLeastSeconds, o.LossAtLeastBasis = est.WindowAtLeastSeconds, est.AtLeastBasis
	if v := est.Objective; v != nil {
		o.Verdict, o.VerdictDetail, o.Gate = v.Status, v.Detail, v.Gate
	}
	return o
}

// unrecordedNote is what a result says when its decision could not be written
// to the audit trail: the decision stands, but it is not on record.
func unrecordedNote(err error) string {
	if err == nil {
		return ""
	}
	return "this decision is not in the audit trail (the audit write failed: " + err.Error() + ")"
}

// recordPlanDecision records that a plan was made for target, and what it said.
// It returns the write error, if any.
func recordPlanDecision(ctx context.Context, target string, plan PITRPlan) error {
	d := audit.RecoveryDecision{
		Stage: audit.RecoveryStagePlan, Decision: audit.RecoveryDecisionPlanned,
		Tool: "plan_point_in_time_recovery", Target: target,
		RecoveryTarget: audit.FormatRecoveryTarget(plan.Target.Type, plan.Target.Value),
		PlanDigest:     plan.PlanDigest, PlanStatus: plan.Status,
		Objectives: objectivesSnapshot(target),
	}
	if plan.Status != PITRFeasible {
		d.Reason = plan.Detail
	}
	if plan.DataLossEstimate != nil || plan.AvailabilityBudget != nil {
		d.Options = []audit.DecisionOption{pitrOption(plan.DataLossEstimate, plan.AvailabilityBudget, false)}
	}
	return toolAuditor.RecordRecoveryDecision(ctx, d)
}

// recordExecutionDecision records what the restore decided just before it
// stopped anything: to go ahead (reason empty) or to refuse (reason). The
// approval facts are what the call carried; the approver's name is known only
// when the approval came through the gateway. It returns the write error, if any.
func recordExecutionDecision(ctx context.Context, args RestoreToPITArgs, plan PITRPlan, est *DataLossEstimate, budget *availability.BudgetVerdict, reason string) error {
	proceeded := reason == ""
	d := audit.RecoveryDecision{
		Stage: audit.RecoveryStageExecution, Tool: "restore_to_point_in_time", Target: args.Target,
		RecoveryTarget: audit.FormatRecoveryTarget(args.RecoveryTargetType, args.RecoveryTargetVal),
		PlanDigest:     args.PlanDigest, PlanStatus: plan.Status,
		Objectives: objectivesSnapshot(args.Target),
		Approval: &audit.ApprovalFacts{
			AllowedStopServing: args.AllowStopServing, AcknowledgedBreach: args.AcknowledgeObjectiveBreach,
		},
	}
	// Which objectives stood in the way (each one demanded the acknowledgement),
	// so that one acknowledgement is not taken to cover a breach it did not name.
	var rpo *RPOComparison
	if est != nil {
		rpo = est.Objective
	}
	d.Approval.Breaches = breachesToAcknowledge(rpo, budget)
	if ap := audit.RecoveryApprovalFromContext(ctx); ap != nil {
		d.Approval.ApprovedBy, d.Approval.AcknowledgedDataLoss = ap.ApprovedBy, ap.Acknowledged
	}
	if proceeded {
		d.Decision = audit.RecoveryDecisionProceeded
	} else {
		d.Decision, d.Reason = audit.RecoveryDecisionRefused, reason
	}
	d.Options = []audit.DecisionOption{pitrOption(est, budget, proceeded)}
	return toolAuditor.RecordRecoveryDecision(ctx, d)
}

// isRefusal says an error from the restore is one of its refusals (every one
// says so), as opposed to a failure once it had begun.
func isRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "refusing to restore")
}

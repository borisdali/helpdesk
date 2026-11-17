package main

// The RECOVERY DECISION section of `vault incidents <run>`: the incident's
// recovery decision read back from its recovery_decision audit events, stage by
// stage. Pure formatting, so it can be checked without a gateway.

import (
	"fmt"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// narrativeRecoveryDecision mirrors the gateway's RecoveryDecisionRecord.
type narrativeRecoveryDecision struct {
	At       time.Time              `json:"at"`
	EventID  string                 `json:"event_id"`
	Decision audit.RecoveryDecision `json:"decision"`
}

func rdSpan(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd %02dh %02dm", s/86400, s%86400/3600, s%3600/60)
}

func rdBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// recoverySummaryLine is the journey header's one-line form: what the trace
// decided and how that stood against the objective.
func recoverySummaryLine(decision, verdict string) string {
	if verdict == "" {
		return decision
	}
	return decision + " (" + verdict + " the recovery objective)"
}

// recoveryDecisionsFromEvents picks the recovery_decision events out of a run's
// events, in order, for formatRecoveryDecisions.
func recoveryDecisionsFromEvents(events []journeyEvent) []narrativeRecoveryDecision {
	var out []narrativeRecoveryDecision
	for _, ev := range events {
		if ev.EventType != string(audit.EventTypeRecoveryDecision) || ev.RecoveryDecision == nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
		out = append(out, narrativeRecoveryDecision{At: at, EventID: ev.EventID, Decision: *ev.RecoveryDecision})
	}
	return out
}

// rdBudget states the availability budget that was in force, from the numbers
// the event recorded (the same wording the config check uses). An event
// recorded without a window (before v0.35, or when no budget could be
// computed) gets no line: the default is not assumed after the fact.
func rdBudget(o *audit.ObjectivesInForce) string {
	if o.AvailabilitySLO == nil || o.AvailabilityWindowSeconds == nil {
		return ""
	}
	var rto time.Duration
	if o.RTOSeconds != nil {
		rto = time.Duration(*o.RTOSeconds) * time.Second
	}
	b := infra.ComputeBudget(*o.AvailabilitySLO, time.Duration(*o.AvailabilityWindowSeconds)*time.Second,
		o.AvailabilityWindowSource, rto, o.RTOSeconds != nil)
	if !b.Computed {
		return ""
	}
	prefix := "  budget: "
	if b.Contradictory {
		prefix = "  ⚠ budget: "
	}
	return prefix + b.Detail
}

// rdObjectives renders the objectives that were in force, with where each came
// from. Nothing is shown for an objective that was not set.
func rdObjectives(o *audit.ObjectivesInForce) []string {
	var lines []string
	head := "Objectives in force"
	if o.Environment != "" {
		head += " (environment " + o.Environment + ")"
	}
	if o.ConfigDigest != "" {
		head += ", config " + o.ConfigDigest
	}
	lines = append(lines, head+":")
	var parts []string
	if o.RPOSeconds != nil {
		parts = append(parts, fmt.Sprintf("rpo %s [%s]", rdSpan(*o.RPOSeconds), o.RPOSource))
	}
	if o.RTOSeconds != nil {
		parts = append(parts, fmt.Sprintf("rto %s [%s]", rdSpan(*o.RTOSeconds), o.RTOSource))
	}
	if o.AvailabilitySLO != nil {
		parts = append(parts, fmt.Sprintf("availability %v%% [%s]", *o.AvailabilitySLO, o.AvailabilitySource))
	}
	if len(parts) == 0 {
		parts = append(parts, "no objective set")
	}
	lines = append(lines, "  "+strings.Join(parts, "   "))
	if line := rdBudget(o); line != "" {
		lines = append(lines, line)
	}
	policy := []string{}
	if o.RecoveryPreference != "" {
		policy = append(policy, fmt.Sprintf("preference %s [%s]", o.RecoveryPreference, o.PreferenceSource))
	}
	enf := "enforcement " + o.Enforcement
	if o.EnforcementSource != "" {
		enf += " [" + o.EnforcementSource + "]"
	}
	lines = append(lines, "  "+strings.Join(append(policy, enf), "   "))
	for _, p := range o.Problems {
		lines = append(lines, "  ⚠ config problem: "+p)
	}
	return lines
}

// rdBasisText says what a restore's assumed duration rested on.
func rdBasisText(basis string) string {
	switch basis {
	case "history_p90", "history": // "history" is a record from before the p90 needed ten samples
		return "the p90 of past restores"
	case "history_longest":
		return "the longest past restore (too few for a p90)"
	}
	return "from the declared rto"
}

func rdOption(opt audit.DecisionOption) string {
	var b strings.Builder
	b.WriteString(opt.Name)
	if opt.Chosen {
		b.WriteString(" (chosen)")
	}
	var loss []string
	if opt.LossSeconds != nil {
		loss = append(loss, rdSpan(*opt.LossSeconds))
	}
	if opt.LossBytes != nil {
		loss = append(loss, rdBytes(*opt.LossBytes)+" of WAL")
	}
	if len(loss) > 0 {
		exact := "exact"
		if opt.LossUpperBound {
			exact = "at most"
		}
		fmt.Fprintf(&b, ": loss %s (%s, %s)", strings.Join(loss, ", "), opt.LossBasis, exact)
	}
	// A lower bound is not exact whatever the basis of the other figures is, so
	// it is said apart from them, in its own words.
	if opt.LossAtLeastSeconds != nil {
		sep := "; "
		if len(loss) == 0 {
			sep = ": loss "
		}
		fmt.Fprintf(&b, "%sat least %s of history", sep, rdSpan(*opt.LossAtLeastSeconds))
	}
	// The transactions counted from the WAL, in the same honesty: a lower bound is
	// said to be one, and a count that could not be made says why.
	switch {
	case opt.LossCommitsStatus == "measured" && opt.LossCommits != nil:
		lead := ""
		if opt.LossCommitsAtLeast {
			lead = "at least "
		}
		fmt.Fprintf(&b, "; %s%d transaction(s) discarded (counted from the WAL)", lead, *opt.LossCommits)
	case opt.LossCommitsStatus == "unavailable":
		fmt.Fprintf(&b, "; transactions not counted (%s)", opt.LossCommitsReason)
	}
	if opt.Verdict != "" {
		fmt.Fprintf(&b, " → %s", opt.Verdict)
	}
	switch opt.Gate {
	case "acknowledge":
		b.WriteString("; needed the breach acknowledged")
	case "refuse":
		b.WriteString("; enforcement refuses it")
	}
	// The same restore set against the downtime budget, said apart from the loss
	// verdict above so the two are never read as one.
	if opt.BudgetVerdict != "" {
		fmt.Fprintf(&b, "; budget → %s", opt.BudgetVerdict)
		if opt.BudgetRemainingSeconds != nil {
			taken := ""
			if opt.BudgetDurationSeconds != nil {
				taken = fmt.Sprintf("; restore taken as %s, %s", rdSpan(*opt.BudgetDurationSeconds), rdBasisText(opt.BudgetDurationBasis))
			}
			fmt.Fprintf(&b, " (%s left%s)", ledgerSpan(*opt.BudgetRemainingSeconds), taken)
		}
		switch opt.BudgetGate {
		case "acknowledge":
			b.WriteString("; the budget needed the breach acknowledged")
		case "refuse":
			b.WriteString("; budget enforcement refuses it")
		}
	}
	// What this database's past restores have taken, when that was known.
	if opt.EstimateStatus == "estimated" && opt.EstimateSamples != nil && (opt.EstimateP90Seconds != nil || opt.EstimateMaxSeconds != nil) {
		if opt.EstimateP90Seconds != nil {
			fmt.Fprintf(&b, "; history: p90 %s over %d past restores", rdSpan(*opt.EstimateP90Seconds), *opt.EstimateSamples)
		} else {
			fmt.Fprintf(&b, "; history: longest %s of %d past restores (too few for a p90)", rdSpan(*opt.EstimateMaxSeconds), *opt.EstimateSamples)
		}
		if opt.EstimateVsRTO == "exceeds" && opt.EstimateOverRTO != nil {
			fmt.Fprintf(&b, ", %d took longer than the rto", *opt.EstimateOverRTO)
		}
	} else if opt.EstimateStatus != "" && opt.EstimateStatus != "estimated" {
		fmt.Fprintf(&b, "; history: %s", opt.EstimateStatus)
	}
	return b.String()
}

// formatRecoveryDecisions renders the section's body, oldest stage first.
func formatRecoveryDecisions(ds []narrativeRecoveryDecision) string {
	if len(ds) == 0 {
		return ""
	}
	var b strings.Builder

	// The objectives, once: from the latest stage that recorded them. If the
	// configuration changed between stages, say so: the stages were not all
	// made against the same requirements.
	var latest *audit.ObjectivesInForce
	digests := map[string]bool{}
	for _, d := range ds {
		if o := d.Decision.Objectives; o != nil {
			latest = o
			if o.ConfigDigest != "" {
				digests[o.ConfigDigest] = true
			}
		}
	}
	if latest != nil {
		for _, l := range rdObjectives(latest) {
			b.WriteString(l + "\n")
		}
		if len(digests) > 1 {
			b.WriteString("  ⚠ the configuration changed between stages: they were not all made against the same objectives\n")
		}
		b.WriteString("\n")
	}

	sawOutcome := false
	for _, d := range ds {
		rd := d.Decision
		stamp := "      –"
		if !d.At.IsZero() {
			stamp = d.At.UTC().Format("15:04:05")
		}
		fmt.Fprintf(&b, "%s  %-9s %-9s", stamp, rd.Stage, rd.Decision)
		switch rd.Stage {
		case audit.RecoveryStageGate:
			who := rd.DecidedBy
			if who == "" {
				who = "unknown"
			}
			fmt.Fprintf(&b, " by %s", who)
			if a := rd.Approval; a != nil {
				var acks []string
				if a.AcknowledgedDataLoss {
					acks = append(acks, "data loss")
				}
				if a.AllowedStopServing {
					acks = append(acks, "stopping the instance")
				}
				if a.AcknowledgedBreach {
					acks = append(acks, "exceeding the objective")
				}
				if len(acks) == 0 {
					acks = append(acks, "nothing beyond approving")
				}
				fmt.Fprintf(&b, "; acknowledged: %s", strings.Join(acks, ", "))
			}
		case audit.RecoveryStageExecution:
			if a := rd.Approval; a != nil && a.ApprovedBy != "" {
				fmt.Fprintf(&b, " on %s's approval", a.ApprovedBy)
			}
			// Which objectives demanded the acknowledgement: one acknowledgement
			// covers them all, and this says what it covered.
			if a := rd.Approval; a != nil && len(a.Breaches) > 0 {
				fmt.Fprintf(&b, "; objectives in the way: %s", strings.Join(a.Breaches, ", "))
			}
		case audit.RecoveryStageOutcome:
			sawOutcome = true
			if rd.DurationSeconds == nil {
				b.WriteString(" recovery duration unknown")
			} else {
				fmt.Fprintf(&b, " recovery duration %s%s", rdSpan(*rd.DurationSeconds), rdRTOVerdict(*rd.DurationSeconds, latest))
			}
		}
		if rd.RecoveryTarget != "" && rd.Stage != audit.RecoveryStageExecution {
			fmt.Fprintf(&b, "  [%s]", rd.RecoveryTarget)
		}
		b.WriteString("\n")
		// Detail lines sit under the decision column (stamp, stage, decision).
		const under = "                    "
		for _, opt := range rd.Options {
			b.WriteString(under + rdOption(opt) + "\n")
		}
		if rd.Reason != "" {
			b.WriteString(under + "reason: " + wordWrap(rd.Reason, 60, under+"        ") + "\n")
		}
	}
	if sawOutcome {
		// Said once, so the figure is not mistaken for downtime.
		b.WriteString("\n  recovery duration is the remediation hop after the approval (restore, restart, verify);\n" +
			"  it is not the downtime, which began earlier and also includes the wait for the approval\n")
		if lines := rdPlanVsActual(ds); len(lines) > 0 {
			b.WriteString("\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	return b.String()
}

// rdDelta says how an actual figure stands against the planned one.
func rdDelta(actual, planned int64, show func(int64) string, more, less string) string {
	switch {
	case actual > planned:
		return show(actual-planned) + " " + more
	case actual < planned:
		return show(planned-actual) + " " + less
	}
	return "as planned"
}

func rdCount(n int64) string { return fmt.Sprintf("%d", n) }

// rdPlanVsActual sets what the plan said against what happened, for a restore
// that has an outcome: the recovery time the budget was checked with against the
// time the remediation took, and the loss and transactions the plan counted
// against the ones measured immediately before the stop. The plan is the last one
// made before the execution, the one an approver would have seen. A line appears
// only when both sides were recorded on the same basis; nothing is estimated here.
func rdPlanVsActual(ds []narrativeRecoveryDecision) []string {
	execIdx, outcomeIdx := -1, -1
	for i, d := range ds {
		switch d.Decision.Stage {
		case audit.RecoveryStageExecution:
			execIdx = i
		case audit.RecoveryStageOutcome:
			outcomeIdx = i
		}
	}
	if outcomeIdx < 0 {
		return nil
	}
	planIdx := -1
	for i, d := range ds {
		if d.Decision.Stage == audit.RecoveryStagePlan && (execIdx < 0 || i < execIdx) {
			planIdx = i
		}
	}
	if planIdx < 0 {
		return nil
	}
	pick := func(rd audit.RecoveryDecision) *audit.DecisionOption {
		for i := range rd.Options {
			if rd.Options[i].Chosen {
				return &rd.Options[i]
			}
		}
		if len(rd.Options) > 0 {
			return &rd.Options[0]
		}
		return nil
	}
	planOpt := pick(ds[planIdx].Decision)
	var execOpt *audit.DecisionOption
	if execIdx >= 0 {
		execOpt = pick(ds[execIdx].Decision)
	}
	var lines []string

	// Recovery time: what the budget check assumed, against what the hop took.
	// Both are the remediation hop after the approval, so they are comparable.
	var planned *int64
	basis := ""
	for _, o := range []*audit.DecisionOption{execOpt, planOpt} {
		if o != nil && o.BudgetDurationSeconds != nil {
			planned, basis = o.BudgetDurationSeconds, o.BudgetDurationBasis
			break
		}
	}
	if actual := ds[outcomeIdx].Decision.DurationSeconds; planned != nil && actual != nil {
		lines = append(lines, fmt.Sprintf("  recovery time  planned %s (%s); took %s (%s)",
			rdSpan(*planned), rdBasisText(basis), rdSpan(*actual), rdDelta(*actual, *planned, rdSpan, "over", "under")))
	}

	if planOpt != nil && execOpt != nil {
		if planOpt.LossSeconds != nil && execOpt.LossSeconds != nil && planOpt.LossBasis == execOpt.LossBasis {
			lines = append(lines, fmt.Sprintf("  data loss      planned %s; at the stop %s (%s)",
				rdSpan(*planOpt.LossSeconds), rdSpan(*execOpt.LossSeconds),
				rdDelta(*execOpt.LossSeconds, *planOpt.LossSeconds, rdSpan, "more", "less")))
		}
		if planOpt.LossCommitsStatus == "measured" && planOpt.LossCommits != nil &&
			execOpt.LossCommitsStatus == "measured" && execOpt.LossCommits != nil {
			atLeast := func(o *audit.DecisionOption) string {
				if o.LossCommitsAtLeast {
					return "at least "
				}
				return ""
			}
			line := fmt.Sprintf("  transactions   planned %s%d; at the stop %s%d",
				atLeast(planOpt), *planOpt.LossCommits, atLeast(execOpt), *execOpt.LossCommits)
			if !planOpt.LossCommitsAtLeast && !execOpt.LossCommitsAtLeast {
				line += " (" + rdDelta(int64(*execOpt.LossCommits), int64(*planOpt.LossCommits), rdCount, "more", "fewer") + ")"
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	head := "  plan vs actual (the plan"
	if at := ds[planIdx].At; !at.IsZero() {
		head += " of " + at.UTC().Format("15:04:05")
	}
	head += " against what happened):"
	return append(append([]string{head}, lines...),
		"  \"at the stop\" is measured immediately before the restore stops the instance; what was",
		"  discarded is not counted afterwards")
}

// rdRTOVerdict sets a recovery duration against the rto that was in force, for
// the outcome line. The comparison is made here, when it is shown, from the
// objectives recorded with the decision; the stored outcome is just the facts.
func rdRTOVerdict(durationSeconds int64, o *audit.ObjectivesInForce) string {
	if o == nil {
		return ""
	}
	if o.RTOSeconds == nil {
		return " (no rto set)"
	}
	rto := *o.RTOSeconds
	src := fmt.Sprintf("rto %s [%s]", rdSpan(rto), o.RTOSource)
	if durationSeconds <= rto {
		return fmt.Sprintf(" (%s: within)", src)
	}
	return fmt.Sprintf(" (%s: exceeded by %s)", src, rdSpan(durationSeconds-rto))
}

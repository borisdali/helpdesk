package main

import (
	"fmt"
	"sort"
	"strings"
)

// CleanReport summarises how many of N repeat runs tripped a verified,
// code-derived warning signal — see hasCleanWarning. This is a distinct axis
// from StabilityReport's outcome-consistency measure: a playbook can be
// perfectly stable (always concludes the same way) while still being
// consistently "dirty" (always missing evidence it should have acted on).
type CleanReport struct {
	FailureID    string
	FailureName  string
	N            int
	WarningCount int
	// WarningDistribution counts occurrences of each warning type across the
	// N runs — a run can trip more than one type, so entries can sum to more
	// than WarningCount. Mirrors AttributionDistribution's shape/purpose:
	// WarningCount alone can't tell an operator which kind of warning fired.
	WarningDistribution map[string]int
}

// warningTypesFor returns which named warning type(s) fired for a single run
// — a run can trip more than one. Objective-evidence entries are keyed by the
// specific signal ("objective_evidence:pod_restarted") when
// ObjectiveEvidenceSignals is populated — this covers both manifestations of
// the same underlying signal (the warn-only path via EvidenceWarnings, and
// the force-gate path via ObjectiveEvidenceGate) since ObjectiveEvidenceSignals
// is populated at both call sites in cmd/gateway/playbooks.go. Falls back to
// the flat "objective_evidence" bucket when EvidenceWarnings/ObjectiveEvidenceGate
// fired but ObjectiveEvidenceSignals is empty, for responses recorded before
// that field existed.
func warningTypesFor(er EvalResult) []string {
	var types []string
	if len(er.ObjectiveEvidenceSignals) > 0 {
		for _, sig := range er.ObjectiveEvidenceSignals {
			types = append(types, "objective_evidence:"+sig)
		}
	} else if len(er.EvidenceWarnings) > 0 || er.ObjectiveEvidenceGate {
		types = append(types, "objective_evidence")
	}
	if er.ProtocolViolation {
		types = append(types, "protocol_violation")
	}
	if er.TargetDrift {
		types = append(types, "target_drift")
	}
	if er.Mismatch {
		// Flat bucket, not tool-keyed: unlike objective_evidence's small fixed
		// vocabulary (pod_restarted/oom_killed), narrated_not_confirmed is an
		// arbitrary list of tool names — keying by tool name would produce an
		// unbounded number of distinct WarningDistribution buckets.
		types = append(types, "mismatch")
	}
	return types
}

func buildCleanReport(f Failure, results []EvalResult) CleanReport {
	r := CleanReport{
		FailureID:   f.ID,
		FailureName: f.Name,
		N:           len(results),
	}
	for _, res := range results {
		if hasCleanWarning(res) {
			r.WarningCount++
		}
		for _, t := range warningTypesFor(res) {
			if r.WarningDistribution == nil {
				r.WarningDistribution = map[string]int{}
			}
			r.WarningDistribution[t]++
		}
	}
	return r
}

// warningDistributionString renders a WarningDistribution map in the same
// "k=v, k=v" style as the existing attribution Distribution line
// (testing/cmd/faulttest/vault.go), sorted for deterministic output.
func warningDistributionString(dist map[string]int) string {
	if len(dist) == 0 {
		return ""
	}
	parts := make([]string, 0, len(dist))
	for k, v := range dist {
		parts = append(parts, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// isClean is zero-tolerance, deliberately: the three signals it counts are
// all verified/code-derived, not self-reported, so there's no natural noise
// floor to justify a percentage threshold the way stabilityPassThreshold/
// stabilityConfThreshold exist for the self-reported outcome axis.
func (r CleanReport) isClean() bool {
	return r.WarningCount == 0
}

// Print writes the clean report to stdout, in the same style as
// StabilityReport.Print — called alongside it, not as a replacement.
func (r CleanReport) Print() {
	fmt.Printf("    Warnings:     %d/%d run(s) tripped a verified warning signal\n", r.WarningCount, r.N)
	if dist := warningDistributionString(r.WarningDistribution); dist != "" {
		fmt.Printf("    Warning types: %s\n", dist)
	}
	if r.isClean() {
		fmt.Printf("    Clean:        yes\n")
	} else {
		fmt.Printf("    Clean:        no\n")
	}
}

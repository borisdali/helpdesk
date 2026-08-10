package main

import "fmt"

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
	}
	return r
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
	if r.isClean() {
		fmt.Printf("    Clean:        yes\n")
	} else {
		fmt.Printf("    Clean:        no\n")
	}
}

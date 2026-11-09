package main

import (
	"fmt"
	"time"

	"helpdesk/testing/faultlib"
)

// recoveryTrailWait and recoveryTrailInterval are how long checkRecoveryTrail
// waits for the gateway's outcome stage; variables so a test need not wait.
var (
	recoveryTrailWait     = faultlib.RecoveryTrailWait
	recoveryTrailInterval = 2 * time.Second
)

// checkRecoveryTrail holds a recovered point-in-time fault to the audit trail a
// restore must leave (faultlib.CheckRecoveryTrail, the same check `go test`
// makes), and fails the fault when it is wrong. It needs --audit-url: without
// one the trail cannot be read, which is reported and does not fail the run, as
// the CLI's other audit-backed evidence does.
func checkRecoveryTrail(cfg *HarnessConfig, f Failure, traceID, runID string, since time.Time, res *EvalResult) {
	if cfg.AuditURL == "" {
		fmt.Printf("Recovery trail:      not checked for %s (set --audit-url to verify the recovery_decision events)\n", f.ID)
		return
	}
	// Every key the stages are filed under: the fault's trace (the plan stage),
	// the remediation hop's trace, which this CLI gives a -remed suffix (the
	// execution stage, written by the remediation agent), and the triage run's
	// ID (the gateway's gate and outcome stages). The go test harness names no
	// suffix, so its remediation shares the fault's trace.
	ids := []string{traceID, traceID + remedTraceSuffix, runID}
	problems := faultlib.AwaitRecoveryTrail(cfg.AuditURL, cfg.GatewayAPIKey, ids, since, recoveryTrailWait, recoveryTrailInterval)
	if len(problems) == 0 {
		fmt.Println("Recovery trail:      plan, gate, execution and outcome recorded; chain verifies")
		return
	}
	res.RecoveryTrailProblems = problems
	res.Passed = false
	for _, p := range problems {
		fmt.Printf("Recovery trail:      FAILED — %s\n", p)
	}
}

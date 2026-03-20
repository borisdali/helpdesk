package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"helpdesk/internal/audit"
)

// stageResult holds the outcome of executing a change against one server.
// Kept for backward compat; internally we now use serverResult from runner.go.
type stageResult struct {
	server string
	output string
	err    error
}

// runStages executes the full staged rollout: canary → waves.
// It returns an error if the canary fails or if the circuit breaker trips.
func runStages(ctx context.Context, rcfg runnerConfig, def *JobDef, servers []string) error {
	if len(servers) == 0 {
		return fmt.Errorf("no servers to process")
	}

	// Approval gate: if any step is write or destructive, require human approval.
	actionClass := jobActionClass(def.Change.Steps)
	if actionClass == audit.ActionWrite || actionClass == audit.ActionDestructive {
		if def.Strategy.DryRun {
			fmt.Printf("APPROVAL WOULD BE REQUIRED: job contains %s operations\n", actionClass)
		} else if rcfg.auditURL != "" {
			approvalID, err := requestFleetJobApproval(ctx, rcfg, def, len(servers))
			if err != nil {
				return fmt.Errorf("failed to request approval: %w", err)
			}
			slog.Info("fleet: approval requested, waiting", "approval_id", approvalID, "timeout_secs", def.Strategy.ApprovalTimeoutSeconds)

			pollInterval := rcfg.approvalPollInterval
			if pollInterval <= 0 {
				pollInterval = 5 * time.Second
			}
			approved, err := waitForFleetApproval(ctx, rcfg, approvalID, def.Strategy.ApprovalTimeoutSeconds, pollInterval)
			if err != nil {
				return fmt.Errorf("approval failed: %w", err)
			}
			if !approved {
				return fmt.Errorf("fleet job denied by approver")
			}
			slog.Info("fleet: approval granted", "approval_id", approvalID)
		}
	}

	strategy := def.Strategy
	canaryCount := strategy.CanaryCount
	if canaryCount > len(servers) {
		canaryCount = len(servers)
	}

	canaryServers := servers[:canaryCount]
	waveServers := servers[canaryCount:]

	// --- Canary phase ---
	slog.Info("fleet: starting canary phase", "job_id", rcfg.jobID, "servers", canaryServers)
	for _, server := range canaryServers {
		res := executeSteps(ctx, rcfg, server, "canary", def.Change.Steps)
		if isServerFailure(res, strategy.CountPartialAsSuccess) {
			slog.Error("fleet: canary failed — aborting job",
				"job_id", rcfg.jobID, "server", server, "err", res.err)
			return fmt.Errorf("canary failed on %s: %w", server, res.err)
		}
		slog.Info("fleet: canary server ok", "job_id", rcfg.jobID, "server", server)
	}

	if len(waveServers) == 0 {
		slog.Info("fleet: no wave servers — job complete", "job_id", rcfg.jobID)
		return nil
	}

	// --- Wave phase ---
	waveSize := strategy.WaveSize
	if waveSize <= 0 {
		waveSize = len(waveServers)
	}

	waves := chunk(waveServers, waveSize)
	slog.Info("fleet: starting wave phase", "job_id", rcfg.jobID, "waves", len(waves))

	for waveIdx, wave := range waves {
		waveName := fmt.Sprintf("wave-%d", waveIdx+1)
		slog.Info("fleet: starting wave", "job_id", rcfg.jobID, "wave", waveName, "servers", len(wave))

		results := runWave(ctx, rcfg, wave, waveName, def.Change.Steps)

		failed := 0
		for _, res := range results {
			if isServerFailure(res, strategy.CountPartialAsSuccess) {
				failed++
				slog.Error("fleet: server failed", "job_id", rcfg.jobID, "wave", waveName,
					"server", res.server, "err", res.err)
			} else {
				slog.Info("fleet: server ok", "job_id", rcfg.jobID, "wave", waveName, "server", res.server)
			}
		}

		// Circuit breaker: abort if failure rate exceeds threshold.
		failureRate := float64(failed) / float64(len(results))
		if failureRate > strategy.FailureThreshold {
			return fmt.Errorf("circuit breaker tripped in %s: %.0f%% failed (threshold %.0f%%)",
				waveName, failureRate*100, strategy.FailureThreshold*100)
		}

		if waveIdx < len(waves)-1 && strategy.WavePauseSeconds > 0 {
			slog.Info("fleet: pausing between waves", "seconds", strategy.WavePauseSeconds)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(strategy.WavePauseSeconds) * time.Second):
			}
		}
	}

	return nil
}

// isServerFailure determines whether a serverResult counts as a failure for
// circuit-breaker and canary purposes.
// If countPartialAsSuccess is true, a "partial" result (continue-on-failure) is not a failure.
func isServerFailure(res serverResult, countPartialAsSuccess bool) bool {
	if res.err != nil {
		return true
	}
	if countPartialAsSuccess {
		return false
	}
	// Check if any step failed (partial = some steps had continue-on-failure errors).
	for _, sr := range res.steps {
		if sr.err != nil {
			return true
		}
	}
	return false
}

// runWave executes steps against all servers in a wave concurrently.
func runWave(ctx context.Context, rcfg runnerConfig, servers []string, waveName string, steps []Step) []serverResult {
	results := make([]serverResult, len(servers))
	var wg sync.WaitGroup

	for i, server := range servers {
		wg.Add(1)
		go func(idx int, srv string) {
			defer wg.Done()
			results[idx] = executeSteps(ctx, rcfg, srv, waveName, steps)
		}(i, server)
	}

	wg.Wait()
	return results
}

// chunk splits a slice into sub-slices of at most size n.
func chunk(s []string, n int) [][]string {
	if n <= 0 || n >= len(s) {
		return [][]string{s}
	}
	var chunks [][]string
	for len(s) > 0 {
		end := n
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}

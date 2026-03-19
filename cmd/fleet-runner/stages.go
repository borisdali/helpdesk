package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// stageResult holds the outcome of executing a change against one server.
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
		output, err := executeChange(ctx, rcfg, server, "canary", def.Change)
		if err != nil {
			slog.Error("fleet: canary failed — aborting job",
				"job_id", rcfg.jobID, "server", server, "err", err)
			return fmt.Errorf("canary failed on %s: %w", server, err)
		}
		slog.Info("fleet: canary server ok", "job_id", rcfg.jobID, "server", server, "output_len", len(output))
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

		results := runWave(ctx, rcfg, wave, waveName, def.Change)

		failed := 0
		for _, res := range results {
			if res.err != nil {
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

// runWave executes a change against all servers in a wave concurrently.
func runWave(ctx context.Context, rcfg runnerConfig, servers []string, waveName string, change Change) []stageResult {
	results := make([]stageResult, len(servers))
	var wg sync.WaitGroup

	for i, server := range servers {
		wg.Add(1)
		go func(idx int, srv string) {
			defer wg.Done()
			output, err := executeChange(ctx, rcfg, srv, waveName, change)
			results[idx] = stageResult{server: srv, output: output, err: err}
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

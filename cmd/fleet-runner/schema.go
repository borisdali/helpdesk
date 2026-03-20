// Package main implements fleet-runner: a one-shot CLI that applies a single
// change across a subset of infrastructure targets with staged rollout.
package main

// JobDef is the top-level fleet job definition, loaded from a JSON file.
type JobDef struct {
	Name     string   `json:"name"`
	Change   Change   `json:"change"`
	Targets  Targets  `json:"targets"`
	Strategy Strategy `json:"strategy"`
}

// Change describes the operation to execute on each target server.
type Change struct {
	// Agent is the target agent type: "database" or "k8s".
	Agent string `json:"agent"`
	// Tool is the tool name, e.g. "run_sql", "vacuum_analyze", "check_connection".
	Tool string `json:"tool"`
	// Args are the tool arguments. The per-target server identifier
	// (connection_string for database, context for k8s) is injected automatically.
	Args map[string]any `json:"args"`
}

// Targets defines which infrastructure servers to include in the job.
type Targets struct {
	// Tags selects servers whose tags contain any of the listed values.
	Tags []string `json:"tags,omitempty"`
	// Names selects servers by their exact infrastructure name.
	Names []string `json:"names,omitempty"`
	// Exclude removes specific server names from the resolved set.
	Exclude []string `json:"exclude,omitempty"`
}

// Strategy controls the staged rollout behaviour.
type Strategy struct {
	// CanaryCount is the number of servers in the canary stage (default 1).
	// A canary failure aborts the entire job immediately.
	CanaryCount int `json:"canary_count"`
	// WaveSize is the number of servers per parallel wave (0 = all remaining).
	WaveSize int `json:"wave_size"`
	// WavePauseSeconds is the pause between waves in seconds (default 0).
	WavePauseSeconds int `json:"wave_pause_seconds"`
	// FailureThreshold is the fraction of failures within a wave that trips the
	// circuit breaker and aborts remaining waves (default 0.5 = 50%).
	FailureThreshold float64 `json:"failure_threshold"`
	// DryRun skips all gateway and auditd contact and only prints the plan.
	DryRun bool `json:"dry_run"`
}

// defaults applies zero-value defaults to Strategy fields.
func (s *Strategy) defaults() {
	if s.CanaryCount <= 0 {
		s.CanaryCount = 1
	}
	if s.FailureThreshold <= 0 {
		s.FailureThreshold = 0.5
	}
}

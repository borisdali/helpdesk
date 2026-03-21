// Package fleet defines the shared schema types for fleet-runner job definitions.
package fleet

// Step is one operation within a multi-step Change sequence.
type Step struct {
	Agent string         `json:"agent"`
	Tool  string         `json:"tool"`
	Args  map[string]any `json:"args,omitempty"`
	// OnFailure controls behaviour when this step fails.
	// "stop" (default): abort this server, mark failed.
	// "continue": log error, proceed to next step.
	OnFailure string `json:"on_failure,omitempty"`
}

// JobDef is the top-level fleet job definition, loaded from a JSON file.
type JobDef struct {
	Name         string   `json:"name"`
	Change       Change   `json:"change"`
	Targets      Targets  `json:"targets"`
	Strategy     Strategy `json:"strategy"`
	// PlanTraceID links this job to the NL planner audit event that generated it.
	// Set by the planner when saving the job definition; carried through to the
	// fleet_jobs audit record so the three auditability questions can be answered:
	// who planned it, was the description changed, and how many plans were attempted.
	PlanTraceID string `json:"plan_trace_id,omitempty"`
}

// Change describes the operation(s) to execute on each target server.
type Change struct {
	// Steps is the multi-step sequence of operations to run on each target.
	Steps []Step `json:"steps,omitempty"`
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
	// CountPartialAsSuccess treats servers with "continue" step failures as
	// successful for circuit-breaker purposes (default false).
	CountPartialAsSuccess bool `json:"count_partial_as_success"`
	// ApprovalTimeoutSeconds is reserved for future approval gate support.
	ApprovalTimeoutSeconds int `json:"approval_timeout_seconds,omitempty"`
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

// Defaults applies zero-value defaults to Strategy fields.
// This is the exported version of defaults().
func (s *Strategy) Defaults() {
	s.defaults()
}


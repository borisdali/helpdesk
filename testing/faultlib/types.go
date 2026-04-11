// Package faultlib provides shared types and functions for fault injection testing.
package faultlib

import (
	"time"
)

// Catalog is the top-level structure of catalog/failures.yaml.
type Catalog struct {
	Version  string    `yaml:"version"`
	Failures []Failure `yaml:"failures"`
}

// Failure describes a single failure mode with injection and evaluation config.
type Failure struct {
	ID          string     `yaml:"id"`
	Name        string     `yaml:"name"`
	Category    string     `yaml:"category"`
	Severity    string     `yaml:"severity"`
	Description string     `yaml:"description"`
	Inject      InjectSpec `yaml:"inject"`
	Teardown    InjectSpec `yaml:"teardown"`
	Prompt      string     `yaml:"prompt"`
	Evaluation  EvalSpec   `yaml:"evaluation"`
	Timeout     string     `yaml:"timeout"`
	// GovernanceGap marks tests that document a known agent behaviour gap rather
	// than asserting correct behaviour.  When the evaluation fails for a
	// governance-gap test, the harness logs the gap but does NOT call t.Errorf,
	// so the suite still passes.  Use this for tests whose purpose is to show
	// *where* the agent falls short, not to enforce that it passes.
	GovernanceGap bool `yaml:"governance_gap,omitempty"`

	// ExternalCompat marks faults that work against any PostgreSQL instance over
	// libpq (no Docker/OS access required). Used to filter the catalog when
	// --external is set.
	ExternalCompat bool `yaml:"external_compat,omitempty"`
	// ExternalInject/ExternalTeardown override Inject/Teardown when running in
	// external mode. Use these to provide SQL-based equivalents for faults that
	// are normally injected via docker_exec or ssh_exec.
	ExternalInject   InjectSpec `yaml:"external_inject,omitempty"`
	ExternalTeardown InjectSpec `yaml:"external_teardown,omitempty"`
	// Remediation defines end-to-end recovery testing: after inject+diagnose,
	// trigger a playbook or agent and verify the database recovers.
	Remediation RemediationSpec `yaml:"remediation,omitempty"`
}

// RemediationSpec describes how to remediate a fault and verify recovery.
type RemediationSpec struct {
	// PlaybookID triggers a fleet playbook: POST /api/v1/fleet/playbooks/{id}/run
	PlaybookID string `yaml:"playbook_id,omitempty"`
	// AgentName is the agent to use for agent-mode remediation (default: "database").
	AgentName string `yaml:"agent_name,omitempty"`
	// AgentPrompt is the prompt sent to the agent for remediation.
	AgentPrompt string `yaml:"agent_prompt,omitempty"`
	// VerifySQL is the SQL query run to confirm recovery (default: "SELECT 1").
	VerifySQL string `yaml:"verify_sql,omitempty"`
	// VerifyTimeout is the max time to wait for recovery (default: "120s").
	VerifyTimeout string `yaml:"verify_timeout,omitempty"`
}

// TimeoutDuration parses the timeout string into a time.Duration.
func (f Failure) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(f.Timeout)
	if err != nil {
		return 120 * time.Second
	}
	return d
}

// InjectSpec describes how to inject or tear down a failure.
type InjectSpec struct {
	Type         string            `yaml:"type"`
	Script       string            `yaml:"script,omitempty"`
	ScriptInline string            `yaml:"script_inline,omitempty"`
	// ExecVia is the container/host target for docker_exec and ssh_exec types.
	// For ssh_exec it is the remote host in "user@host" or "host" form.
	ExecVia      string            `yaml:"exec_via,omitempty"`
	// User is the OS user to run the script as in docker_exec mode (e.g., "postgres").
	User         string            `yaml:"user,omitempty"`
	Action       string            `yaml:"action,omitempty"`
	Service      string            `yaml:"service,omitempty"`
	Signal       string            `yaml:"signal,omitempty"`
	Overlay      string            `yaml:"overlay,omitempty"`
	Restore      interface{}       `yaml:"restore,omitempty"`
	Target       string            `yaml:"target,omitempty"`
	Override     map[string]string `yaml:"override,omitempty"`
	Detach       bool              `yaml:"detach,omitempty"`
	// Wait is an optional duration to sleep after the injection action completes
	// (e.g. "30s"). Useful for kustomize overlays that trigger a rolling update:
	// the sleep lets the pod enter its failure state before the agent prompt is sent.
	Wait         string            `yaml:"wait,omitempty"`
}

// EvalSpec describes how to evaluate the agent's response.
type EvalSpec struct {
	ExpectedTools     []string      `yaml:"expected_tools"`
	ExpectedKeywords  KeywordSpec   `yaml:"expected_keywords"`
	ExpectedDiagnosis DiagnosisSpec `yaml:"expected_diagnosis"`
	// ExpectedToolOrder is an optional list of ordered pairs [[tool_a, tool_b], ...].
	// Each pair asserts that evidence of tool_a appears before evidence of tool_b
	// in the agent's response text. When set, Passed requires OrderingPass=true.
	ExpectedToolOrder [][]string `yaml:"expected_tool_order,omitempty"`
}

// KeywordSpec defines expected keywords with synonym tolerance.
type KeywordSpec struct {
	AnyOf []string `yaml:"any_of"`
}

// DiagnosisSpec defines the expected diagnosis category.
type DiagnosisSpec struct {
	Category string `yaml:"category"`
}

// HarnessConfig holds runtime configuration for the test harness.
type HarnessConfig struct {
	CatalogPath      string
	TestingDir       string
	ConnStr          string
	ReplicaConnStr   string
	DBAgentURL       string
	K8sAgentURL      string
	SysadminAgentURL string
	OrchestratorURL  string
	KubeContext      string
	Categories       []string
	FailureIDs       []string

	// External enables external PG mode: only external_compat faults are run,
	// and ExternalInject/ExternalTeardown specs are used instead of Inject/Teardown.
	External bool
	// RemediateEnabled runs the remediation phase after injection + diagnosis.
	RemediateEnabled bool
	// GatewayURL is the helpdesk gateway base URL for playbook/agent remediation.
	GatewayURL string
	// GatewayAPIKey is the Bearer token for gateway/auditd auth during remediation.
	GatewayAPIKey string
	// InfraConfigPath is the path to infrastructure.json for tag safety checks.
	// When set, the harness refuses to inject faults unless the target has a
	// "test" or "chaos" tag.
	InfraConfigPath string
	// SSHHost is the default SSH target for ssh_exec faults when exec_via is empty
	// (e.g., "ubuntu@customer-vm.example.com" or just "customer-vm").
	// When set, ExternalInject/ExternalTeardown are used instead of Inject/Teardown.
	SSHHost string
	// SSHUser is the SSH username for ssh_exec faults (default: current user).
	SSHUser string
	// SSHKeyPath is the SSH private key path for ssh_exec faults.
	SSHKeyPath string
}

// EvalResult contains the evaluation outcome for a single failure test.
type EvalResult struct {
	FailureID     string  `json:"failure_id"`
	FailureName   string  `json:"failure_name"`
	Category      string  `json:"category"`
	Score         float64 `json:"score"`
	Passed        bool    `json:"passed"`
	KeywordPass   bool    `json:"keyword_pass"`
	DiagnosisPass bool    `json:"diagnosis_pass"`
	ToolEvidence  bool    `json:"tool_evidence"`
	// OrderingPass is true when all ExpectedToolOrder pairs are satisfied
	// (tool_a evidence precedes tool_b evidence in the response text).
	// Always true when ExpectedToolOrder is empty.
	OrderingPass bool    `json:"ordering_pass"`
	ResponseText string  `json:"response_text"`
	Duration     string  `json:"duration"`
	Error        string  `json:"error,omitempty"`

	// Remediation outcome fields (populated only when RemediateEnabled=true).
	RemediationAttempted bool    `json:"remediation_attempted,omitempty"`
	RemediationPassed    bool    `json:"remediation_passed,omitempty"`
	RecoveryTimeSecs     float64 `json:"recovery_time_seconds,omitempty"`
	RemediationError     string  `json:"remediation_error,omitempty"`
}

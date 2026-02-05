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
}

// TimeoutDuration parses the timeout string into a time.Duration.
func (f Failure) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(f.Timeout)
	if err != nil {
		return 60 * time.Second
	}
	return d
}

// InjectSpec describes how to inject or tear down a failure.
type InjectSpec struct {
	Type         string            `yaml:"type"`
	Script       string            `yaml:"script,omitempty"`
	ScriptInline string            `yaml:"script_inline,omitempty"`
	ExecVia      string            `yaml:"exec_via,omitempty"`
	Action       string            `yaml:"action,omitempty"`
	Service      string            `yaml:"service,omitempty"`
	Overlay      string            `yaml:"overlay,omitempty"`
	Restore      interface{}       `yaml:"restore,omitempty"`
	Target       string            `yaml:"target,omitempty"`
	Override     map[string]string `yaml:"override,omitempty"`
	Detach       bool              `yaml:"detach,omitempty"`
}

// EvalSpec describes how to evaluate the agent's response.
type EvalSpec struct {
	ExpectedTools     []string      `yaml:"expected_tools"`
	ExpectedKeywords  KeywordSpec   `yaml:"expected_keywords"`
	ExpectedDiagnosis DiagnosisSpec `yaml:"expected_diagnosis"`
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
	CatalogPath     string
	TestingDir      string
	ConnStr         string
	ReplicaConnStr  string
	DBAgentURL      string
	K8sAgentURL     string
	OrchestratorURL string
	KubeContext     string
	Categories      []string
	FailureIDs      []string
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
	ResponseText  string  `json:"response_text"`
	Duration      string  `json:"duration"`
	Error         string  `json:"error,omitempty"`
}

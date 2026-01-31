package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	ExpectedTools     []string          `yaml:"expected_tools"`
	ExpectedKeywords  KeywordSpec       `yaml:"expected_keywords"`
	ExpectedDiagnosis DiagnosisSpec     `yaml:"expected_diagnosis"`
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
	CatalogPath    string
	TestingDir     string
	ConnStr        string
	ReplicaConnStr string
	DBAgentURL     string
	K8sAgentURL    string
	OrchestratorURL string
	KubeContext    string
	Categories     []string
	FailureIDs     []string
}

// LoadCatalog reads and parses the failure catalog YAML file.
func LoadCatalog(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog: %v", err)
	}

	var catalog Catalog
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("parsing catalog: %v", err)
	}

	if catalog.Version == "" {
		return nil, fmt.Errorf("catalog missing version field")
	}

	return &catalog, nil
}

// FilterFailures returns failures matching the given categories and/or IDs.
func FilterFailures(catalog *Catalog, categories, ids []string) []Failure {
	if len(categories) == 0 && len(ids) == 0 {
		return catalog.Failures
	}

	catSet := make(map[string]bool, len(categories))
	for _, c := range categories {
		catSet[c] = true
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	var result []Failure
	for _, f := range catalog.Failures {
		if len(idSet) > 0 && idSet[f.ID] {
			result = append(result, f)
			continue
		}
		if len(catSet) > 0 && catSet[f.Category] {
			result = append(result, f)
		}
	}
	return result
}

// ResolvePrompt replaces template variables in the failure prompt.
func ResolvePrompt(prompt string, cfg *HarnessConfig) string {
	r := strings.NewReplacer(
		"{{connection_string}}", cfg.ConnStr,
		"{{replica_connection_string}}", cfg.ReplicaConnStr,
		"{{kube_context}}", cfg.KubeContext,
	)
	return r.Replace(prompt)
}

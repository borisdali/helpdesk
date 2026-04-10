package main

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	GovernanceGap bool `yaml:"governance_gap,omitempty"`

	// ExternalCompat marks faults that work against any PostgreSQL instance over
	// libpq (no Docker/OS access required).
	ExternalCompat   bool       `yaml:"external_compat,omitempty"`
	ExternalInject   InjectSpec `yaml:"external_inject,omitempty"`
	ExternalTeardown InjectSpec `yaml:"external_teardown,omitempty"`
	Remediation      RemediationSpec `yaml:"remediation,omitempty"`
}

// RemediationSpec describes how to remediate a fault and verify recovery.
type RemediationSpec struct {
	PlaybookID    string `yaml:"playbook_id,omitempty"`
	AgentName     string `yaml:"agent_name,omitempty"`
	AgentPrompt   string `yaml:"agent_prompt,omitempty"`
	VerifySQL     string `yaml:"verify_sql,omitempty"`
	VerifyTimeout string `yaml:"verify_timeout,omitempty"`
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
	// ExecVia is the container/host target for docker_exec and ssh_exec types.
	// For ssh_exec it is the remote host in "user@host" or "host" form.
	ExecVia      string            `yaml:"exec_via,omitempty"`
	Action       string            `yaml:"action,omitempty"`
	Service      string            `yaml:"service,omitempty"`
	Signal       string            `yaml:"signal,omitempty"`
	Overlay      string            `yaml:"overlay,omitempty"`
	Restore      interface{}       `yaml:"restore,omitempty"`
	Target       string            `yaml:"target,omitempty"`
	Override     map[string]string `yaml:"override,omitempty"`
	Detach       bool              `yaml:"detach,omitempty"`
	Wait         string            `yaml:"wait,omitempty"`
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
	InfraConfigPath string
	// SSHUser is the SSH username for ssh_exec faults.
	SSHUser string
	// SSHKeyPath is the SSH private key path for ssh_exec faults.
	SSHKeyPath string
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
// When cfg.External is true, only faults marked external_compat are included.
func FilterFailures(catalog *Catalog, cfg *HarnessConfig) []Failure {
	categories := cfg.Categories
	ids := cfg.FailureIDs

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
		// External mode: skip faults that don't work without Docker/OS access.
		if cfg.External && !f.ExternalCompat {
			continue
		}

		if len(categories) == 0 && len(ids) == 0 {
			result = append(result, f)
			continue
		}
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

// infraConfig is a minimal representation of infrastructure.json for tag checking.
type infraConfig struct {
	DBServers map[string]struct {
		ConnectionString string   `json:"connection_string"`
		Tags             []string `json:"tags"`
	} `json:"db_servers"`
}

// checkTargetSafety verifies that the target PostgreSQL host (extracted from
// connStr) has a "test" or "chaos" tag in infrastructure.json. This prevents
// accidental fault injection against production databases.
//
// When infraConfigPath is empty the check is skipped (opt-out).
func checkTargetSafety(infraConfigPath, connStr string) error {
	if infraConfigPath == "" {
		return nil
	}

	data, err := os.ReadFile(infraConfigPath)
	if err != nil {
		return fmt.Errorf("reading infra config: %v", err)
	}

	var cfg infraConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing infra config: %v", err)
	}

	// Extract host from the connection string.
	// Handles both DSN ("host=... port=...") and URL ("postgres://host:port/db") formats.
	targetHost := connStrHost(connStr)
	if targetHost == "" {
		return fmt.Errorf("cannot extract host from connection string %q", connStr)
	}

	for name, srv := range cfg.DBServers {
		srvHost := connStrHost(srv.ConnectionString)
		if srvHost != targetHost {
			continue
		}
		// Matched — check tags.
		for _, tag := range srv.Tags {
			if tag == "test" || tag == "chaos" {
				return nil
			}
		}
		return fmt.Errorf("target host %q (server %q) does not have a 'test' or 'chaos' tag — "+
			"refusing to inject faults. Add tag in infrastructure.json to opt-in", targetHost, name)
	}

	// Host not found in infra config — refuse by default.
	return fmt.Errorf("target host %q not found in infrastructure config %q — "+
		"refusing to inject faults. Add it with a 'test' or 'chaos' tag to opt-in", targetHost, infraConfigPath)
}

// connStrHost extracts the hostname from a libpq connection string (DSN or URL).
func connStrHost(connStr string) string {
	// Try URL format first.
	if strings.HasPrefix(connStr, "postgres://") || strings.HasPrefix(connStr, "postgresql://") {
		u, err := url.Parse(connStr)
		if err == nil {
			return u.Hostname()
		}
	}

	// DSN format: "host=... port=... dbname=..."
	for _, part := range strings.Fields(connStr) {
		if strings.HasPrefix(part, "host=") {
			return strings.TrimPrefix(part, "host=")
		}
	}
	return ""
}

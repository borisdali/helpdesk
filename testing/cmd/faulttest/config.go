package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"helpdesk/testing/catalog"
)

// Catalog is the top-level structure of catalog/failures.yaml.
type Catalog struct {
	Version  string    `yaml:"version"`
	Failures []Failure `yaml:"failures"`
}

// Failure describes a single failure mode with injection and evaluation config.
type Failure struct {
	ID            string     `yaml:"id"`
	Name          string     `yaml:"name"`
	Category      string     `yaml:"category"`
	Severity      string     `yaml:"severity"`
	Description   string     `yaml:"description"`
	Prerequisites string     `yaml:"prerequisites,omitempty"`
	Inject      InjectSpec `yaml:"inject"`
	Teardown    InjectSpec `yaml:"teardown"`
	Prompt      string     `yaml:"prompt"`
	Evaluation  EvalSpec   `yaml:"evaluation"`
	Timeout     string     `yaml:"timeout"`
	GovernanceGap bool `yaml:"governance_gap,omitempty"`

	// ExternalCompat marks faults that work against any PostgreSQL instance over
	// libpq (no Docker/OS access required).
	ExternalCompat   bool       `yaml:"external_compat,omitempty"`
	// DiagnosisPlaybookSeriesID links this fault to a gateway playbook for
	// diagnosis. When set and --via-gateway is active, faulttest calls
	// POST /api/v1/fleet/playbooks/{id}/run instead of the agent directly.
	DiagnosisPlaybookSeriesID string `yaml:"diagnosis_playbook_series_id,omitempty"`
	ExternalInject   InjectSpec `yaml:"external_inject,omitempty"`
	ExternalTeardown InjectSpec `yaml:"external_teardown,omitempty"`
	Remediation RemediationSpec `yaml:"remediation,omitempty"`

	// Source is set programmatically to "builtin" or "custom". It is never
	// read from or written to YAML — the yaml:"-" tag ensures that.
	Source string `yaml:"-"`
}

// RemediationSpec describes how to remediate a fault and verify recovery.
type RemediationSpec struct {
	PlaybookID    string `yaml:"playbook_id,omitempty"`
	AgentName     string `yaml:"agent_name,omitempty"`
	AgentPrompt   string `yaml:"agent_prompt,omitempty"`
	VerifySQL     string `yaml:"verify_sql,omitempty"`
	VerifyTimeout string `yaml:"verify_timeout,omitempty"`
}

// IsAutoDBCompat reports whether faulttest can inject this fault against a
// temporary Docker PostgreSQL it spins up itself (--auto-db mode).
func (f Failure) IsAutoDBCompat() bool {
	if !f.ExternalCompat || f.Category == "kubernetes" {
		return false
	}
	t := f.ExternalInject.Type
	if t == "" {
		t = f.Inject.Type
	}
	return t != "ssh_exec"
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
	Category  string `yaml:"category"`
	Narrative string `yaml:"narrative,omitempty"`
}

// HarnessConfig holds runtime configuration for the test harness.
type HarnessConfig struct {
	CatalogPath      string
	TestingDir       string
	ConnStr          string
	ReplicaConnStr   string
	AgentConnStr     string // overrides ConnStr in prompt {{connection_string}} when set
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
	// AutoDB instructs faulttest to spin up a temporary Docker PostgreSQL and use
	// it as the injection target. Implies External=true. Only auto-db-compat faults run.
	AutoDB bool
	// AutoDBContainerName is the docker container name for the auto-db instance
	// (e.g. "faulttest-auto-db-deadbeef"). Set by main after startAutoDBContainer
	// returns; exposed as $FAULTTEST_CONTAINER to shell_exec inject/teardown scripts.
	AutoDBContainerName string
	// Repeat is the number of inject→triage→teardown cycles to run per fault.
	// Values > 1 enable stability testing: remediation is skipped and a
	// StabilityReport is printed after all cycles complete. Default 1.
	Repeat int
	// RemediateEnabled runs the remediation phase after injection + diagnosis.
	RemediateEnabled bool
	// GatewayURL is the helpdesk gateway base URL for playbook/agent remediation.
	GatewayURL string
	// GatewayAPIKey is the Bearer token for gateway/auditd auth during remediation.
	GatewayAPIKey string
	// SysadminAPIKey is the Bearer token for the sysadmin agent's /tool/ endpoint.
	// Required when HELPDESK_USERS_FILE is set on the sysadmin agent (service-account auth).
	// Create a service account in the sysadmin's users.yaml and pass its API key here.
	SysadminAPIKey string
	// GatewayPurpose is the declared purpose sent in gateway requests (default: "diagnostic").
	GatewayPurpose string
	// ApprovalMode overrides the playbook's default approval_mode for this run.
	// Values: "manual", "session", "auto", "force". Empty = use playbook default.
	// Use "force" to bypass manual gates in automated/CI faulttest runs.
	ApprovalMode string
	// OperatorID is the user identity sent as X-User on gateway requests.
	// Must match a user in users.yaml with roles required for the run
	// (e.g. dba_lead or oncall_senior to bypass approval_override_roles clamping).
	OperatorID string
	// UsersFile is the path to users.yaml. When set and --approval-mode force is used,
	// the harness validates that OperatorID exists as a human user in that file before
	// calling ProceedEscalation. Prevents fake identities from appearing in the audit log.
	UsersFile string
	// InfraConfigPath is the path to infrastructure.json for tag safety checks.
	InfraConfigPath string
	// SSHHost is the default SSH target for ssh_exec faults when exec_via is empty
	// (e.g., "ubuntu@customer-vm.example.com" or just "customer-vm").
	// When set, ExternalInject/ExternalTeardown are used instead of Inject/Teardown.
	SSHHost string
	// SSHUser is the SSH username for ssh_exec faults.
	SSHUser string
	// SSHKeyPath is the SSH private key path for ssh_exec faults.
	SSHKeyPath string

	// CustomCatalogs is the list of additional customer catalog file paths,
	// populated by repeated --catalog flags.
	CustomCatalogs []string
	// SourceFilter restricts which faults are run: "" (all), "builtin", or "custom".
	SourceFilter string
	// ReportDir is the directory where the JSON report is written (default: ".").
	ReportDir string
	// ReportPerFault writes an individual JSON report per fault in addition to the
	// combined report. Files are named faulttest-{runID}-{faultID}.json.
	ReportPerFault bool

	// DiagnosisModel is the model used by the triage agent to generate diagnoses.
	// Recorded as an annotation in the stability cert so the cert is self-describing.
	// Defaults to HELPDESK_MODEL_NAME (the env var that configures the agent server).
	DiagnosisModel string

	// JudgeEnabled enables LLM-as-judge diagnosis scoring.
	JudgeEnabled bool
	// RemediationJudgeEnabled enables LLM-as-judge remediation approach scoring.
	// Reuses the same judge LLM config (JudgeModel/JudgeVendor/JudgeAPIKey).
	// Only meaningful when --remediate is also set.
	RemediationJudgeEnabled bool
	// JudgeModel is the model name for the LLM judge (default: HELPDESK_MODEL_NAME).
	JudgeModel string
	// JudgeVendor is the model vendor for the LLM judge (default: HELPDESK_MODEL_VENDOR).
	JudgeVendor string
	// JudgeAPIKey is the API key for the LLM judge (default: HELPDESK_API_KEY).
	JudgeAPIKey string

	// AuditURL is the base URL of the audit service (e.g. "http://localhost:7070").
	// When set, the harness queries tool execution events after each agent call
	// to get structured tool evidence from the audit trail.
	AuditURL string

	// NotifyURL is an optional webhook URL. When set, faulttest POSTs the full
	// JSON report to this URL after the run completes (e.g. a Slack webhook).
	NotifyURL string

	// ViaGateway routes the diagnosis call through the gateway's playbook
	// endpoint instead of calling the agent directly, when the fault has a
	// DiagnosisPlaybookSeriesID and GatewayURL is set.
	// Enables a valid A/B comparison between scaffolded (normal) and
	// crystal-ball (unguided) gateway runs.
	ViaGateway bool

	// GateEscalation sends gate_escalation=true on every PlaybookRun request so
	// the gateway intercepts ESCALATE_TO at the phase boundary.
	GateEscalation bool
	// EmitAndWait replaces TTY prompts with HTTP polling when true:
	//   - gate: polls GET /api/v1/fleet/playbook-runs/{id} until outcome changes
	//   - step: uses the audit service long-poll instead of /dev/tty
	EmitAndWait bool
}

// LoadCatalog reads and parses the failure catalog YAML file.
// Each failure's Source is stamped as "custom".
func LoadCatalog(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog: %v", err)
	}
	return LoadCatalogFromBytes(data, "custom")
}

// LoadBuiltinCatalog parses the embedded built-in catalog.
// Each failure's Source is stamped as "builtin".
func LoadBuiltinCatalog() (*Catalog, error) {
	return LoadCatalogFromBytes(catalog.BuiltinYAML, "builtin")
}

// LoadCatalogFromBytes parses YAML bytes and stamps each failure with the given
// source label ("builtin" or "custom"). The version field check is skipped for
// custom catalogs so customers may omit it.
func LoadCatalogFromBytes(data []byte, source string) (*Catalog, error) {
	var c Catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing catalog: %v", err)
	}
	if source == "builtin" && c.Version == "" {
		return nil, fmt.Errorf("built-in catalog missing version field")
	}
	for i := range c.Failures {
		c.Failures[i].Source = source
	}
	return &c, nil
}

// LoadAndMergeCatalogs loads the built-in catalog and appends each custom
// catalog file. All duplicate-ID errors are collected before returning.
func LoadAndMergeCatalogs(customPaths []string) (*Catalog, error) {
	base, err := LoadBuiltinCatalog()
	if err != nil {
		return nil, err
	}
	return mergeCustomInto(base, customPaths)
}

// mergeCustomInto appends faults from each custom file into base and returns
// the merged catalog. All duplicate-ID errors are collected before returning.
func mergeCustomInto(base *Catalog, paths []string) (*Catalog, error) {
	seen := make(map[string]string) // id → source label
	for i := range base.Failures {
		seen[base.Failures[i].ID] = base.Failures[i].Source
	}

	var errs []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading custom catalog %s: %v", path, err)
		}
		custom, err := LoadCatalogFromBytes(data, "custom")
		if err != nil {
			return nil, fmt.Errorf("parsing custom catalog %s: %v", path, err)
		}
		for _, f := range custom.Failures {
			if prev, dup := seen[f.ID]; dup {
				errs = append(errs, fmt.Sprintf("duplicate fault ID %q (first seen in %s, also in %s)", f.ID, prev, path))
				continue
			}
			seen[f.ID] = path
			base.Failures = append(base.Failures, f)
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("catalog merge errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return base, nil
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
		// Auto-DB mode: only faults that work against a spun-up Docker PostgreSQL.
		if cfg.AutoDB && !f.IsAutoDBCompat() {
			continue
		}
		// External mode: skip faults that don't work without Docker/OS access.
		if cfg.External && !cfg.AutoDB && !f.ExternalCompat {
			continue
		}
		// Source filter: "builtin" or "custom".
		if cfg.SourceFilter != "" && f.Source != cfg.SourceFilter {
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
// {{connection_string}} resolves to AgentConnStr when set, falling back to ConnStr.
// This allows --agent-conn to decouple the injection DSN (used by psql) from
// the identifier sent to the agent (which may be a registered alias like "test-db").
func ResolvePrompt(prompt string, cfg *HarnessConfig) string {
	connStr := cfg.ConnStr
	if cfg.AgentConnStr != "" {
		connStr = cfg.AgentConnStr
	}
	r := strings.NewReplacer(
		"{{connection_string}}", connStr,
		"{{replica_connection_string}}", cfg.ReplicaConnStr,
		"{{kube_context}}", cfg.KubeContext,
	)
	return r.Replace(prompt)
}

// infraConfig is a minimal representation of infrastructure.json for tag checking
// and alias resolution.
type infraConfig struct {
	DBServers map[string]struct {
		ConnectionString string   `json:"connection_string"`
		PasswordEnv      string   `json:"password_env"`
		Tags             []string `json:"tags"`
	} `json:"db_servers"`
}

// resolveConnAlias resolves a named infra key (e.g. "faulttest-db") to its
// actual connection string. Returns connStr unchanged when infraConfigPath is
// empty, connStr is already a DSN/URL, or the key is not found.
func resolveConnAlias(infraConfigPath, connStr string) string {
	if infraConfigPath == "" || connStr == "" {
		return connStr
	}
	// If it looks like a DSN or URL, no resolution needed.
	if strings.Contains(connStr, "=") || strings.Contains(connStr, "://") {
		return connStr
	}
	data, err := os.ReadFile(infraConfigPath)
	if err != nil {
		return connStr
	}
	var cfg infraConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return connStr
	}
	srv, ok := cfg.DBServers[connStr]
	if !ok {
		return connStr
	}
	cs := srv.ConnectionString
	if srv.PasswordEnv != "" {
		if pw := os.Getenv(srv.PasswordEnv); pw != "" {
			cs += " password=" + pw
		}
	}
	return cs
}

// checkTargetSafety verifies that the target PostgreSQL host (extracted from
// connStr) has a "test" or "chaos" tag in infrastructure.json. This prevents
// accidental fault injection against production databases.
//
// When infraConfigPath is empty the check is skipped (opt-out).
func checkTargetSafety(infraConfigPath, connStr string) error {
	if infraConfigPath == "" || connStr == "" {
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

	// Fast path: connStr may be a named infra key (e.g. "alloydb-on-vm").
	// Look it up directly before falling back to host-based matching.
	if srv, ok := cfg.DBServers[connStr]; ok {
		for _, tag := range srv.Tags {
			if tag == "test" || tag == "chaos" {
				return nil
			}
		}
		return fmt.Errorf("server %q does not have a 'test' or 'chaos' tag — "+
			"refusing to inject faults. Add tag in infrastructure.json to opt-in", connStr)
	}

	// Extract host from the connection string.
	// Handles both DSN ("host=... port=...") and URL ("postgres://host:port/db") formats.
	targetHost := connStrHost(connStr)
	if targetHost == "" {
		return fmt.Errorf("cannot extract host from connection string %q", connStr)
	}

	// Scan ALL entries matching the target host. Pass if ANY has the required
	// tag — multiple entries can share the same hostname (e.g. alloydb-on-vm
	// and alloydb-on-vm-local both resolve to localhost). Go map iteration is
	// non-deterministic, so we must not short-circuit on the first match.
	var matched []string
	for name, srv := range cfg.DBServers {
		srvHost := connStrHost(srv.ConnectionString)
		if srvHost != targetHost {
			continue
		}
		matched = append(matched, name)
		for _, tag := range srv.Tags {
			if tag == "test" || tag == "chaos" {
				return nil
			}
		}
	}

	if len(matched) == 0 {
		// Host not found in infra config — refuse by default.
		return fmt.Errorf("target host %q not found in infrastructure config %q — "+
			"refusing to inject faults. Add it with a 'test' or 'chaos' tag to opt-in", targetHost, infraConfigPath)
	}

	// One or more entries matched but none had test/chaos tag.
	return fmt.Errorf("target host %q (server(s) %v) does not have a 'test' or 'chaos' tag — "+
		"refusing to inject faults. Add tag in infrastructure.json to opt-in", targetHost, matched)
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

// logConnResolution logs a single INFO line showing what alias (if any) a conn
// flag resolved to. Skipped when the flag was empty. When the value changed
// (alias was expanded), logs alias→host. When unchanged (raw DSN passed
// directly), logs just the host so the operator can still verify the target.
func logConnResolution(flag, before, after string) {
	if after == "" {
		return
	}
	host := connStrHost(after)
	if host == "" {
		host = after // fallback: log the raw value if we can't parse a host
	}
	if before != after {
		slog.Info(flag, "alias", before, "host", host)
	} else {
		slog.Info(flag, "host", host)
	}
}

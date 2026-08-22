package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"helpdesk/testing/catalog"
	"helpdesk/testing/faultlib"
)

// Catalog, Failure, and their component types are aliases for faultlib's
// canonical definitions (item 7 dedup, v0.26) — this package previously
// carried a full parallel copy of each, which had already drifted from
// faultlib's: ExpectedToolOrder was missing from this copy's EvalSpec
// (silently dropped for any catalog fault authored against this package's
// schema), and TimeoutDuration()'s fallback default had drifted to 60s here
// vs. faultlib's 120s — moot in practice since faultlib.Runner.TimeoutDuration
// was the only one ever called in production (via toLFFailure's conversion),
// but a real trap for anyone reading this package's copy of the method.
// Aliasing (not just importing) means every existing `Failure{...}`/`[]Failure`
// call site in this package and its tests keeps compiling unchanged — the
// alias makes them the literal same type as faultlib's, not just
// structurally identical, so IsAutoDBCompat/TimeoutDuration are inherited
// automatically and never need a local redefinition to drift again.
type (
	Catalog         = faultlib.Catalog
	Failure         = faultlib.Failure
	RemediationSpec = faultlib.RemediationSpec
	InjectSpec      = faultlib.InjectSpec
	EvalSpec        = faultlib.EvalSpec
	KeywordSpec     = faultlib.KeywordSpec
	DiagnosisSpec   = faultlib.DiagnosisSpec
)

// HarnessConfig holds runtime configuration for the test harness. Embeds
// faultlib.HarnessConfig (item 7 dedup, v0.26) rather than duplicating its
// ~34 fields — this package previously carried a fully independent copy,
// hand-mapped field-by-field into faultlib.HarnessConfig via a since-deleted
// toLFConfig. That mapping had already silently drifted: faultlib's
// GatewayPollInterval had no counterpart here at all, making it permanently
// unconfigurable from this CLI (always defaulting to 15s) with no compiler
// error to catch the gap. Embedding makes that class of drift a compile
// error instead of a silent one — a field added to faultlib.HarnessConfig is
// automatically available here too, and vice versa there's nothing to keep
// in sync.
//
// Only 5 fields below are genuinely CLI-only concerns faultlib.Runner/
// Injector/Remediator never need: Repeat/ReportPerFault (the CLI's own
// --repeat outer loop and per-fault reporting), DiagnosisModel (a cert
// annotation, not consumed by faultlib), SysadminAPIKey (a header this CLI
// sets on its own registration calls, not part of any faultlib request),
// and RemediationJudgeEnabled (gates a judge call this CLI's own evaluator
// makes directly, not something faultlib.Remediator triggers).
type HarnessConfig struct {
	faultlib.HarnessConfig

	// Repeat is the number of inject→triage→teardown cycles to run per fault.
	// Values > 1 enable stability testing: remediation is skipped and a
	// StabilityReport is printed after all cycles complete. Default 1.
	Repeat int
	// ReportPerFault writes an individual JSON report per fault in addition to the
	// combined report. Files are named faulttest-{runID}-{faultID}.json.
	ReportPerFault bool
	// DiagnosisModel is the model used by the triage agent to generate diagnoses.
	// Recorded as an annotation in the stability cert so the cert is self-describing.
	// Defaults to HELPDESK_MODEL_NAME (the env var that configures the agent server).
	DiagnosisModel string
	// SysadminAPIKey is the Bearer token for the sysadmin agent's /tool/ endpoint.
	// Required when HELPDESK_USERS_FILE is set on the sysadmin agent (service-account auth).
	// Create a service account in the sysadmin's users.yaml and pass its API key here.
	SysadminAPIKey string
	// RemediationJudgeEnabled enables LLM-as-judge remediation approach scoring.
	// Reuses the same judge LLM config (JudgeModel/JudgeVendor/JudgeAPIKey).
	// Only meaningful when --remediate is also set.
	RemediationJudgeEnabled bool
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

	excludeSet := make(map[string]bool, len(cfg.ExcludeIDs))
	for _, id := range cfg.ExcludeIDs {
		excludeSet[id] = true
	}

	var result []Failure
	for _, f := range catalog.Failures {
		// Exclude list wins over everything else — applied first so an
		// excluded ID can never sneak back in via categories or an allowlist.
		if excludeSet[f.ID] {
			continue
		}
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

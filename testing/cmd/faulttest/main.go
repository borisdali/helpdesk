// Command faulttest injects database and Kubernetes failure modes, sends
// diagnostic prompts to helpdesk agents, and evaluates their responses.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// stringSliceFlag is a repeatable flag.Value for --catalog.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string        { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error    { *s = append(*s, v); return nil }

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "list":
		cmdList(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "inject":
		cmdInject(os.Args[2:])
	case "teardown":
		cmdTeardown(os.Args[2:])
	case "validate":
		cmdValidate(os.Args[2:])
	case "example":
		cmdExample(os.Args[2:])
	case "show":
		cmdShow(os.Args[2:])
	case "vault":
		cmdVault(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: faulttest <command> [options]

Commands:
  list       List all failure modes in the catalog
  run        Inject failures, run agent, evaluate, teardown
  inject     Inject a specific failure (interactive mode)
  teardown   Tear down a specific failure (interactive mode)
  validate   Validate a customer catalog file for errors and warnings
  example    Print an annotated example customer catalog entry to stdout
  show       Print a fault definition as YAML (pipe to a file to customize it)
  vault      Fault↔playbook pairing table, pass rate trends, drift detection
`)
}

// defaultTestingDir returns the testing/ directory relative to the binary or cwd.
func defaultTestingDir() string {
	// Try relative to cwd.
	if _, err := os.Stat("testing/catalog/failures.yaml"); err == nil {
		return "testing"
	}
	// Try parent.
	if _, err := os.Stat("../catalog/failures.yaml"); err == nil {
		return ".."
	}
	return "testing"
}

func loadConfig(fs *flag.FlagSet, args []string) *HarnessConfig {
	cfg := &HarnessConfig{}

	fs.StringVar(&cfg.TestingDir, "testing-dir", defaultTestingDir(), "Path to the testing/ directory")
	fs.StringVar(&cfg.ConnStr, "conn", "", "PostgreSQL connection string (used for injection)")
	fs.StringVar(&cfg.ReplicaConnStr, "replica-conn", "", "Replica PostgreSQL connection string")
	fs.StringVar(&cfg.AgentConnStr, "agent-conn", "", "Connection string or alias sent to the agent in prompts (defaults to --conn)")
	fs.StringVar(&cfg.DBAgentURL, "db-agent", "", "Database agent A2A URL")
	fs.StringVar(&cfg.K8sAgentURL, "k8s-agent", "", "Kubernetes agent A2A URL")
	fs.StringVar(&cfg.SysadminAgentURL, "sysadmin-agent", "", "Sysadmin agent A2A URL")
	fs.StringVar(&cfg.OrchestratorURL, "orchestrator", "", "Orchestrator agent A2A URL")
	fs.StringVar(&cfg.KubeContext, "context", "", "Kubernetes context")

	var categories, ids string
	fs.StringVar(&categories, "categories", "", "Comma-separated categories to test (database,kubernetes,compound)")
	fs.StringVar(&ids, "ids", "", "Comma-separated failure IDs to test")

	// External PG mode.
	fs.BoolVar(&cfg.External, "external", false, "Only run external_compat faults using libpq (no Docker/OS access needed)")

	// SSH injection backend.
	fs.StringVar(&cfg.SSHHost, "ssh-host", "", "SSH target for ssh_exec faults (user@host or host); triggers ExternalInject mode")
	fs.StringVar(&cfg.SSHUser, "ssh-user", os.Getenv("USER"), "SSH username for ssh_exec faults (prepended to host when no @ in --ssh-host)")
	fs.StringVar(&cfg.SSHKeyPath, "ssh-key", "", "SSH private key path for ssh_exec faults")

	// Remediation phase.
	fs.BoolVar(&cfg.RemediateEnabled, "remediate", false, "Run remediation phase after injection+diagnosis")
	fs.StringVar(&cfg.GatewayURL, "gateway", "", "Gateway URL for playbook/agent remediation and vault playbook checks")
	fs.StringVar(&cfg.GatewayAPIKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key for remediation (or HELPDESK_CLIENT_API_KEY)")
	fs.StringVar(&cfg.GatewayPurpose, "purpose", "diagnostic", "Purpose declared in gateway requests (diagnostic, remediation, maintenance, …)")

	// Policy safety check.
	fs.StringVar(&cfg.InfraConfigPath, "infra-config", "", "Path to infrastructure.json; when set, target must have a 'test' or 'chaos' tag")

	// Customer catalog support.
	var extraCatalogs stringSliceFlag
	fs.Var(&extraCatalogs, "catalog", "Additional customer catalog file (repeatable)")
	fs.StringVar(&cfg.SourceFilter, "source", "", "Filter faults by source: builtin or custom")
	fs.StringVar(&cfg.ReportDir, "report-dir", ".", "Directory to write the JSON report (default: current directory)")

	// LLM judge options.
	fs.BoolVar(&cfg.JudgeEnabled, "judge", false, "Enable LLM-as-judge for semantic diagnosis scoring")
	fs.StringVar(&cfg.JudgeModel, "judge-model", "", "Model name for judge (default: HELPDESK_MODEL_NAME env var)")
	fs.StringVar(&cfg.JudgeVendor, "judge-vendor", "", "Model vendor for judge: anthropic or google (default: HELPDESK_MODEL_VENDOR env var)")
	fs.StringVar(&cfg.JudgeAPIKey, "judge-api-key", "", "API key for judge (default: HELPDESK_API_KEY env var)")

	// Audit-based tool evidence.
	fs.StringVar(&cfg.AuditURL, "audit-url", "", "Audit service base URL (e.g. http://localhost:7070); when set, tool evidence uses audit trail")

	// Completion webhook.
	fs.StringVar(&cfg.NotifyURL, "notify-url", "", "Webhook URL for POST notification on run completion (e.g. Slack incoming webhook)")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Track which flags were explicitly set by the caller.
	explicitFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	cfg.CustomCatalogs = []string(extraCatalogs)

	if categories != "" {
		cfg.Categories = strings.Split(categories, ",")
	}
	if ids != "" {
		cfg.FailureIDs = strings.Split(ids, ",")
	}

	// Auto-detect filesystem mode: if the catalog file exists on disk, use it.
	// Otherwise the embedded catalog is used (standalone binary mode).
	detectedPath := filepath.Join(cfg.TestingDir, "catalog", "failures.yaml")
	if _, err := os.Stat(detectedPath); err == nil {
		cfg.CatalogPath = detectedPath
	}

	// In embedded mode (standalone binary, no source tree) the internal Docker
	// and kustomize injection infrastructure is unavailable. Default --external
	// to true so customers don't get a flood of "injection failed" errors from
	// non-SQL faults. The caller can still override with --external=false.
	if cfg.CatalogPath == "" && !explicitFlags["external"] {
		cfg.External = true
	}

	testutil.DockerComposeDir = filepath.Join(cfg.TestingDir, "docker")

	return cfg
}

// loadActiveCatalog loads the catalog appropriate to the current mode:
//   - Filesystem mode (dev/CI): parse the file on disk, merge custom catalogs.
//   - Embedded mode (standalone binary): use the embedded built-in, merge custom catalogs.
func loadActiveCatalog(cfg *HarnessConfig) (*Catalog, error) {
	if cfg.CatalogPath != "" {
		base, err := LoadCatalog(cfg.CatalogPath)
		if err != nil {
			return nil, err
		}
		// Stamp source on all built-in entries (LoadCatalog sets "custom"; correct it).
		for i := range base.Failures {
			base.Failures[i].Source = "builtin"
		}
		if len(cfg.CustomCatalogs) == 0 {
			return base, nil
		}
		return mergeCustomInto(base, cfg.CustomCatalogs)
	}
	// Standalone binary: use embedded catalog.
	return LoadAndMergeCatalogs(cfg.CustomCatalogs)
}

// ── list ─────────────────────────────────────────────────────────────────

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	failures := FilterFailures(cat, cfg)

	fmt.Printf("%-30s %-12s %-10s %-8s %-8s %s\n", "ID", "CATEGORY", "SEVERITY", "EXTERNAL", "SOURCE", "NAME")
	fmt.Println(strings.Repeat("-", 100))
	for _, f := range failures {
		ext := ""
		if f.ExternalCompat {
			ext = "yes"
		}
		fmt.Printf("%-30s %-12s %-10s %-8s %-8s %s\n", f.ID, f.Category, f.Severity, ext, f.Source, f.Name)
	}
	fmt.Printf("\nTotal: %d failure modes\n", len(failures))
}

// ── run ──────────────────────────────────────────────────────────────────

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	failures := FilterFailures(cat, cfg)
	if len(failures) == 0 {
		fmt.Fprintln(os.Stderr, "No failures match the specified filters.")
		os.Exit(1)
	}

	// Policy safety check: verify the target has a test/chaos tag.
	if err := checkTargetSafety(cfg.InfraConfigPath, cfg.ConnStr); err != nil {
		fmt.Fprintf(os.Stderr, "Safety check failed: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	injector := NewInjector(cfg)
	runner := NewRunner(cfg)
	remediator := NewRemediator(cfg)

	// Initialize LLM judge completer if enabled.
	var judgeCompleter faultlib.TextCompleter
	var judgeModel string
	if cfg.JudgeEnabled {
		var err error
		judgeCompleter, err = newJudgeCompleter(ctx, cfg)
		if err != nil {
			slog.Warn("LLM judge disabled: could not initialize", "err", err)
		} else {
			judgeModel = cfg.JudgeModel
			if judgeModel == "" {
				judgeModel = os.Getenv("HELPDESK_MODEL_NAME")
			}
			slog.Info("LLM judge enabled", "vendor", cfg.JudgeVendor, "model", judgeModel)
		}
	}

	runID := uuid.New().String()[:8]
	var results []EvalResult

	for _, f := range failures {
		fmt.Printf("\n--- Testing: %s (%s) ---\n", f.Name, f.ID)

		// Skip faults that require a replica when none is configured.
		if faultNeedsReplica(f) && cfg.ReplicaConnStr == "" {
			slog.Warn("skipping fault: requires --replica-conn", "id", f.ID)
			fmt.Printf("Result: [SKIP] replica connection not configured (pass --replica-conn)\n")
			continue
		}

		// Save original conn string for config-override failures.
		origConn := cfg.ConnStr

		// 1. Inject.
		if err := injector.Inject(ctx, f); err != nil {
			slog.Error("injection failed", "id", f.ID, "err", err)
			results = append(results, EvalResult{
				FailureID:   f.ID,
				FailureName: f.Name,
				Category:    f.Category,
				Error:       fmt.Sprintf("injection failed: %v", err),
			})
			cfg.ConnStr = origConn
			continue
		}

		// 2. Run agent (record call start for audit window).
		callStart := time.Now()
		resp := runner.Run(ctx, f)

		// Query audit trail for tool evidence if --audit-url is set.
		var auditTools []string
		if cfg.AuditURL != "" {
			auditTools = auditQueryTools(ctx, cfg.AuditURL, cfg.GatewayAPIKey, callStart)
			if len(auditTools) > 0 {
				slog.Info("audit evidence", "failure", f.ID, "tools", auditTools)
			}
		}

		// 3. Evaluate.
		var evalResult EvalResult
		if resp.Error != nil {
			fmt.Printf("Agent error: %v\n", resp.Error)
			evalResult = EvalResult{
				FailureID:   f.ID,
				FailureName: f.Name,
				Category:    f.Category,
				Error:       resp.Error.Error(),
				Duration:    resp.Duration.String(),
			}
		} else {
			if judgeCompleter != nil {
				evalResult = EvaluateWithJudge(ctx, f, resp, judgeCompleter, judgeModel, auditTools)
			} else {
				evalResult = Evaluate(f, resp, auditTools)
			}
			evalResult.ResponseText = resp.Text
			evalResult.Duration = resp.Duration.String()
		}

		// 4. Remediation phase (optional).
		if cfg.RemediateEnabled && (f.Remediation.PlaybookID != "" || f.Remediation.AgentPrompt != "") {
			remResult := remediator.Remediate(ctx, f)
			evalResult.RemediationAttempted = true
			evalResult.RemediationPassed = remResult.Passed
			evalResult.RecoveryTimeSecs = remResult.RecoveryTimeSecs
			evalResult.RemediationScore = remResult.Score
			evalResult.RemediationMethod = remResult.Method
			if remResult.Err != nil {
				evalResult.RemediationError = remResult.Err.Error()
			}
			// OverallScore: 60% composite score + 40% remediation when remediation attempted.
			evalResult.OverallScore = evalResult.Score*0.6 + remResult.Score*0.4
			if remResult.Passed {
				fmt.Printf("Remediation: RECOVERED in %.1fs (score: %.0f%%)\n", remResult.RecoveryTimeSecs, remResult.Score*100)
				// Auto-suggest: when remediation succeeds and a gateway is configured,
				// synthesize a playbook draft from the fault trace and save it to the vault.
				if cfg.GatewayURL != "" {
					traceID := "faulttest-" + runID + "-" + f.ID
					if pbID, vaultErr := requestVaultDraft(ctx, cfg, traceID, "resolved"); vaultErr != nil {
						slog.Warn("vault: could not generate playbook draft", "fault", f.ID, "err", vaultErr)
					} else if pbID != "" {
						fmt.Printf("Vault: draft saved → %s (activate with 'faulttest vault list')\n", pbID)
					}
				}
			} else {
				fmt.Printf("Remediation: FAILED — %v\n", remResult.Err)
			}
		} else {
			// No remediation attempted: overall score equals diagnosis score.
			evalResult.OverallScore = evalResult.Score
		}

		results = append(results, evalResult)

		// 5. Teardown.
		cfg.ConnStr = origConn
		if err := injector.Teardown(ctx, f); err != nil {
			slog.Error("teardown failed", "id", f.ID, "err", err)
		}

		status := "PASS"
		if !evalResult.Passed {
			status = "FAIL"
		}
		fmt.Printf("Diagnostic Result:   [%s] score=%d%% (keywords=%d%% tools=%d%% category=%d%%)\n",
			status, int(evalResult.Score*100),
			int(evalResult.KeywordScore*100), int(evalResult.ToolScore*100), int(evalResult.DiagnosisScore*100))
		if evalResult.RemediationAttempted {
			remStatus := "PASS"
			if !evalResult.RemediationPassed {
				remStatus = "FAIL"
			}
			fmt.Printf("Remediation Result:  [%s] score=%d%% (%.1fs, %s)\n",
				remStatus, int(evalResult.RemediationScore*100), evalResult.RecoveryTimeSecs, evalResult.RemediationMethod)
			fmt.Printf("Overall Result:      [%s] score=%d%%\n", status, int(evalResult.OverallScore*100))
		}
	}

	report := BuildReport(runID, results)
	report.PrintSummary()

	reportFile := fmt.Sprintf("%s/faulttest-%s.json", cfg.ReportDir, runID)
	if err := report.WriteJSON(reportFile); err != nil {
		slog.Error("failed to write report", "err", err)
	} else {
		fmt.Printf("Report written to %s\n", reportFile)
	}

	if cfg.NotifyURL != "" {
		postNotify(cfg.NotifyURL, report)
	}

	// Append run to persistent history for vault commands.
	// Use the agent-conn alias when set (human-readable, no password);
	// fall back to the hostname extracted from the injection conn string.
	target := cfg.AgentConnStr
	if target == "" {
		target = connStrHost(cfg.ConnStr)
	}
	if err := appendHistory(report, target); err != nil {
		slog.Warn("failed to append to run history", "err", err, "path", historyFilePath())
	}
}

// postNotify POSTs the full JSON report to the given webhook URL. Failures are
// logged at Warn level and never cause the faulttest run to fail.
func postNotify(notifyURL string, report Report) {
	body, err := json.Marshal(report)
	if err != nil {
		slog.Warn("notify: failed to marshal report", "err", err)
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, notifyURL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("notify: failed to build request", "url", notifyURL, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("notify: HTTP request failed", "url", notifyURL, "err", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode >= 300 {
		slog.Warn("notify: webhook returned non-2xx", "status", resp.StatusCode, "url", notifyURL)
		return
	}
	slog.Info("notify: webhook notified", "url", notifyURL, "status", resp.StatusCode)
}

// requestVaultDraft POSTs to the gateway's from-trace endpoint to synthesize a
// playbook draft from the given traceID and saves it to the vault as an inactive
// draft. Returns the persisted playbook_id, or "" when auditd is not configured.
func requestVaultDraft(ctx context.Context, cfg *HarnessConfig, traceID, outcome string) (string, error) {
	reqBody, err := json.Marshal(map[string]string{
		"trace_id": traceID,
		"outcome":  outcome,
	})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	reqURL := strings.TrimSuffix(cfg.GatewayURL, "/") + "/api/v1/fleet/playbooks/from-trace"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GatewayAPIKey)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST from-trace: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var result struct {
		PlaybookID string `json:"playbook_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.PlaybookID, nil
}

// ── inject ───────────────────────────────────────────────────────────────

func cmdInject(args []string) {
	fs := flag.NewFlagSet("inject", flag.ExitOnError)
	var failureID string
	fs.StringVar(&failureID, "id", "", "Failure ID to inject")
	cfg := loadConfig(fs, args)

	if failureID == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	f := findFailure(cat, failureID)
	if f == nil {
		fmt.Fprintf(os.Stderr, "Error: failure %q not found\n", failureID)
		os.Exit(1)
	}

	if err := checkTargetSafety(cfg.InfraConfigPath, cfg.ConnStr); err != nil {
		fmt.Fprintf(os.Stderr, "Safety check failed: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	injector := NewInjector(cfg)

	if err := injector.Inject(ctx, *f); err != nil {
		fmt.Fprintf(os.Stderr, "Injection failed: %v\n", err)
		os.Exit(1)
	}

	prompt := ResolvePrompt(f.Prompt, cfg)
	fmt.Printf("Failure injected: %s\n\n", f.Name)
	fmt.Printf("Suggested prompt for the agent:\n%s\n", prompt)
	fmt.Printf("\nTo tear down: faulttest teardown --id %s [same flags]\n", f.ID)
}

// ── teardown ─────────────────────────────────────────────────────────────

func cmdTeardown(args []string) {
	fs := flag.NewFlagSet("teardown", flag.ExitOnError)
	var failureID string
	fs.StringVar(&failureID, "id", "", "Failure ID to tear down")
	cfg := loadConfig(fs, args)

	if failureID == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	f := findFailure(cat, failureID)
	if f == nil {
		fmt.Fprintf(os.Stderr, "Error: failure %q not found\n", failureID)
		os.Exit(1)
	}

	if err := checkTargetSafety(cfg.InfraConfigPath, cfg.ConnStr); err != nil {
		fmt.Fprintf(os.Stderr, "Safety check failed: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	injector := NewInjector(cfg)

	if err := injector.Teardown(ctx, *f); err != nil {
		fmt.Fprintf(os.Stderr, "Teardown failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Failure torn down: %s\n", f.Name)
}

// ── validate ─────────────────────────────────────────────────────────────

var knownInjectTypes = map[string]bool{
	"sql": true, "docker": true, "docker_exec": true,
	"kustomize": true, "kustomize_delete": true,
	"config": true, "ssh_exec": true, "shell_exec": true,
}

var knownCategories = map[string]bool{
	"database": true, "kubernetes": true, "host": true, "compound": true,
}

func cmdValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	if len(cfg.CustomCatalogs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one --catalog file is required")
		os.Exit(1)
	}

	// Load the built-in catalog to check for duplicate IDs.
	builtinCat, err := loadActiveCatalog(&HarnessConfig{CatalogPath: cfg.CatalogPath})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading built-in catalog: %v\n", err)
		os.Exit(1)
	}
	builtinIDs := make(map[string]bool, len(builtinCat.Failures))
	for _, f := range builtinCat.Failures {
		builtinIDs[f.ID] = true
	}

	totalErrors, totalWarnings := 0, 0

	// seenIDs accumulates across all custom files to detect cross-file duplicates.
	seenIDs := make(map[string]string) // id → first file that defined it

	for _, path := range cfg.CustomCatalogs {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", path, err)
			os.Exit(1)
		}
		custom, err := LoadCatalogFromBytes(data, "custom")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", path, err)
			os.Exit(1)
		}

		fmt.Printf("Validating %s (%d entries):\n", path, len(custom.Failures))

		fileErrors, fileWarnings := 0, 0

		for _, f := range custom.Failures {
			var errs, warns []string

			// Required fields.
			if f.ID == "" {
				errs = append(errs, "missing id")
			}
			if f.Name == "" {
				errs = append(errs, "missing name")
			}
			if f.Category == "" {
				errs = append(errs, "missing category")
			}
			if f.Inject.Type == "" {
				errs = append(errs, "missing inject.type")
			}

			// Known inject type.
			if f.Inject.Type != "" && !knownInjectTypes[f.Inject.Type] {
				errs = append(errs, fmt.Sprintf("inject.type %q is not a known type", f.Inject.Type))
			}

			// Duplicate ID check — within this file and across all custom files.
			if f.ID != "" {
				if builtinIDs[f.ID] {
					errs = append(errs, fmt.Sprintf("duplicate ID %q conflicts with built-in catalog", f.ID))
				} else if prev, ok := seenIDs[f.ID]; ok {
					if prev == path {
						errs = append(errs, fmt.Sprintf("duplicate ID %q within this file", f.ID))
					} else {
						errs = append(errs, fmt.Sprintf("duplicate ID %q already defined in %s", f.ID, prev))
					}
				} else {
					seenIDs[f.ID] = path
				}
			}

			// Script file existence check (only when testing-dir is set).
			if f.Inject.Script != "" && cfg.CatalogPath != "" {
				scriptPath := filepath.Join(cfg.TestingDir, f.Inject.Script)
				if _, err := os.Stat(scriptPath); err != nil {
					errs = append(errs, fmt.Sprintf("inject.script %q not found at %s", f.Inject.Script, scriptPath))
				}
			} else if f.Inject.Script != "" {
				warns = append(warns, "inject.script referenced but no --testing-dir set; cannot verify file exists")
			}

			// Warnings.
			if f.Category != "" && !knownCategories[f.Category] {
				warns = append(warns, fmt.Sprintf("category %q is not a known category (database, kubernetes, host, compound)", f.Category))
			}
			if len(f.Evaluation.ExpectedKeywords.AnyOf) == 0 {
				warns = append(warns, "no expected_keywords; scoring will be unreliable")
			}

			// Playbook existence check (when --gateway is set).
			if cfg.GatewayURL != "" && f.Remediation.PlaybookID != "" {
				switch checkPlaybook(cfg.GatewayURL, cfg.GatewayAPIKey, f.Remediation.PlaybookID) {
				case playbookNotFound:
					warns = append(warns, fmt.Sprintf("playbook %q not found at gateway %s", f.Remediation.PlaybookID, cfg.GatewayURL))
				case playbookAuthError:
					warns = append(warns, fmt.Sprintf("playbook %q: authentication failed (check --api-key)", f.Remediation.PlaybookID))
				}
			}

			label := f.ID
			if label == "" {
				label = "(no id)"
			}

			switch {
			case len(errs) > 0:
				for _, e := range errs {
					fmt.Printf("  [ERR]  %s: %s\n", label, e)
					fileErrors++
				}
				for _, w := range warns {
					fmt.Printf("  [WARN] %s: %s\n", label, w)
					fileWarnings++
				}
			case len(warns) > 0:
				for _, w := range warns {
					fmt.Printf("  [WARN] %s: %s\n", label, w)
					fileWarnings++
				}
			default:
				fmt.Printf("  [OK]   %s\n", label)
			}
		}

		totalErrors += fileErrors
		totalWarnings += fileWarnings
	}

	fmt.Printf("\n%d error(s), %d warning(s).\n", totalErrors, totalWarnings)
	if totalErrors > 0 {
		os.Exit(1)
	}
}

// validatePlaybookExists checks whether a playbook with the given series_id exists
// on the gateway. Returns true when the playbook is found, false otherwise.
// Network failures or unexpected status codes are treated as "not found" (returns false).
type playbookCheckResult int

const (
	playbookFound     playbookCheckResult = iota
	playbookNotFound                      // 404 or empty list
	playbookAuthError                     // 401/403
	playbookUnknown                       // network error or unexpected status
)

// checkPlaybook queries the gateway to determine whether a playbook series exists.
func checkPlaybook(gatewayURL, apiKey, playbookID string) playbookCheckResult {
	reqURL := gatewayURL + "/api/v1/fleet/playbooks?series_id=" + playbookID

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return playbookUnknown
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return playbookUnknown
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return playbookAuthError
	case http.StatusNotFound:
		return playbookNotFound
	case http.StatusOK:
		// Fall through to parse body.
	default:
		slog.Warn("playbook check: unexpected status", "playbook_id", playbookID, "status", resp.StatusCode)
		return playbookUnknown
	}

	var result struct {
		Playbooks []map[string]interface{} `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return playbookUnknown
	}
	if len(result.Playbooks) == 0 {
		return playbookNotFound
	}
	return playbookFound
}

// ── example ──────────────────────────────────────────────────────────────

func cmdExample(args []string) {
	fs := flag.NewFlagSet("example", flag.ExitOnError)
	var category string
	fs.StringVar(&category, "category", "database", "Category for the example entry (database, kubernetes, host, compound)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch category {
	case "database":
		os.Stdout.WriteString(exampleDatabase)
	case "kubernetes":
		os.Stdout.WriteString(exampleKubernetes)
	case "host":
		os.Stdout.WriteString(exampleHost)
	case "compound":
		os.Stdout.WriteString(exampleCompound)
	default:
		fmt.Fprintf(os.Stderr, "Unknown category %q; use database, kubernetes, host, or compound\n", category)
		os.Exit(1)
	}
}

const exampleDatabase = `# Custom fault catalog — database example
# Save to a file (e.g. my-faults.yaml) and run:
#   faulttest validate --catalog my-faults.yaml
#   faulttest list    --catalog my-faults.yaml
#   faulttest inject  --catalog my-faults.yaml --id my-slow-query --conn "host=..."

failures:
  - id: my-slow-query               # Unique ID — must not clash with built-in IDs
    name: "Custom: Slow query storm"
    category: database              # database | kubernetes | host | compound
    severity: high                  # critical | high | medium | low
    description: >
      Simulates a storm of long-running queries using pg_sleep.
      The database agent should detect the blocked sessions and recommend termination.

    # How to inject the failure.
    inject:
      type: sql                     # sql | docker | docker_exec | kustomize | kustomize_delete | config | ssh_exec | shell_exec
      script_inline: |
        SELECT pg_sleep(300);       -- run this in a background session outside this YAML

    # How to restore normal state after the test.
    teardown:
      type: sql
      script_inline: |
        SELECT pg_terminate_backend(pid)
        FROM pg_stat_activity
        WHERE state = 'active'
          AND query LIKE '%pg_sleep%'
          AND pid <> pg_backend_pid();

    # Prompt sent to the agent after injection.
    prompt: |
      There seems to be a performance problem on {{connection_string}}.
      Can you investigate?

    timeout: "120s"                 # Per-fault deadline (default: 60s)

    evaluation:
      expected_tools:
        - list_long_running_queries # Tools the agent should call
        - terminate_connection
      expected_keywords:
        any_of:
          - "long-running"          # At least one keyword must appear in response
          - "slow query"
          - "pg_sleep"
          - "terminate"
      expected_diagnosis:
        category: performance       # Expected diagnosis label

    # Mark true to document a known agent gap without failing CI.
    # governance_gap: false
`

const exampleKubernetes = `# Custom fault catalog — kubernetes example
failures:
  - id: my-pod-oom
    name: "Custom: Pod OOMKilled"
    category: kubernetes
    severity: high
    description: >
      Simulates an OOMKilled pod by setting an extremely low memory limit.

    inject:
      type: kustomize
      overlay: catalog/overlays/my-oom-overlay   # path relative to --testing-dir

    teardown:
      type: kustomize_delete
      overlay: catalog/overlays/my-oom-overlay
      restore: catalog/overlays/base             # re-apply base after delete

    prompt: |
      A pod in the cluster seems to be crashing repeatedly.
      Context: {{kube_context}}. Please investigate.

    timeout: "180s"

    evaluation:
      expected_tools:
        - list_pods
        - describe_pod
      expected_keywords:
        any_of:
          - "OOMKilled"
          - "out of memory"
          - "memory limit"
      expected_diagnosis:
        category: resource
`

const exampleHost = `# Custom fault catalog — host/OS example
failures:
  - id: my-disk-full
    name: "Custom: Disk full on data volume"
    category: host
    severity: critical
    description: >
      Fills the PostgreSQL data volume to trigger write failures.

    # ssh_exec runs the script on the target VM via SSH.
    # Pass --ssh-host or --ssh-key when running.
    inject:
      type: ssh_exec
      exec_via: "ubuntu@db-host.example.com"   # or leave empty and pass --ssh-host
      script_inline: |
        #!/bin/bash
        fallocate -l 10G /var/lib/postgresql/data/fill.bin

    teardown:
      type: ssh_exec
      exec_via: "ubuntu@db-host.example.com"
      script_inline: |
        #!/bin/bash
        rm -f /var/lib/postgresql/data/fill.bin

    prompt: |
      PostgreSQL on {{connection_string}} is reporting write errors.
      Can you diagnose the root cause?

    timeout: "120s"
    external_compat: false        # Requires SSH access to the host

    evaluation:
      expected_tools:
        - check_disk_space
      expected_keywords:
        any_of:
          - "disk full"
          - "no space left"
          - "storage"
      expected_diagnosis:
        category: storage
`

const exampleCompound = `# Custom fault catalog — compound (multi-step) example
failures:
  - id: my-primary-failover
    name: "Custom: Primary failover under load"
    category: compound
    severity: critical
    description: >
      Kills the primary while writes are in-flight; expects the agent to detect
      promotion of the replica and re-point clients.

    inject:
      type: shell_exec
      script_inline: |
        #!/bin/bash
        set -e
        docker stop my-pg-primary
        sleep 2
        # Trigger replica promotion
        docker exec my-pg-replica pg_ctl promote -D /var/lib/postgresql/data

    teardown:
      type: shell_exec
      script_inline: |
        #!/bin/bash
        docker start my-pg-primary
        sleep 5
        # Re-sync replica (simplified)
        docker stop my-pg-replica && docker start my-pg-replica

    prompt: |
      We've had a failover event. Primary connection string: {{connection_string}}.
      Replica: {{replica_connection_string}}. Please assess the situation.

    timeout: "240s"

    evaluation:
      expected_tools:
        - get_server_info
        - list_replication_slots
      expected_keywords:
        any_of:
          - "failover"
          - "primary"
          - "replica"
          - "promoted"
          - "standby"
      expected_diagnosis:
        category: availability
`

// faultNeedsReplica reports whether any inject/teardown spec in the fault
// targets the replica, meaning --replica-conn is required to run it.
func faultNeedsReplica(f Failure) bool {
	for _, spec := range []InjectSpec{f.Inject, f.Teardown, f.ExternalInject, f.ExternalTeardown} {
		if spec.Target == "replica" {
			return true
		}
	}
	return false
}

func findFailure(cat *Catalog, id string) *Failure {
	for i := range cat.Failures {
		if cat.Failures[i].ID == id {
			return &cat.Failures[i]
		}
	}
	return nil
}

// ── show ─────────────────────────────────────────────────────────────────

// cmdShow prints a fault definition as YAML so customers can clone and
// modify it. The output is a valid single-entry catalog that passes validate.
func cmdShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	var failureID string
	fs.StringVar(&failureID, "id", "", "Fault ID to show")
	cfg := loadConfig(fs, args)

	if failureID == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	f := findFailure(cat, failureID)
	if f == nil {
		fmt.Fprintf(os.Stderr, "Error: fault %q not found\n", failureID)
		os.Exit(1)
	}

	// Wrap in a minimal catalog structure so the output is directly usable
	// as a --catalog file after the customer assigns a new ID.
	type catalogOut struct {
		Version  string    `yaml:"version"`
		Failures []Failure `yaml:"failures"`
	}
	out := catalogOut{Version: "1", Failures: []Failure{*f}}

	data, err := yaml.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error serializing fault: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("# Cloned from built-in fault %q. Change the id before using as a custom catalog.\n", failureID)
	fmt.Print(string(data))
}

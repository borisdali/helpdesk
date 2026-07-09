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

	"helpdesk/internal/buildinfo"
	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// stringSliceFlag is a repeatable flag.Value for --catalog.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(buildinfo.Version)
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
  version    Print the build version and exit
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

// reorderArgs moves positional (non-flag) args after flag args so that Go's
// flag package can parse flags regardless of where positional args appear.
// For example "vault versions pbs_foo --gateway http://x" becomes valid input.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	type boolFlag interface{ IsBoolFlag() bool }
	valuedFlags := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(boolFlag); !ok || !bf.IsBoolFlag() {
			valuedFlags[f.Name] = true
		}
	})
	var flagArgs, posArgs []string
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			posArgs = append(posArgs, arg)
			i++
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if eq := strings.Index(name, "="); eq >= 0 {
			name = name[:eq]
		}
		flagArgs = append(flagArgs, arg)
		i++
		if valuedFlags[name] && !strings.Contains(arg, "=") && i < len(args) && !strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
			i++
		}
	}
	return append(flagArgs, posArgs...)
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
	fs.StringVar(&cfg.ConnStr, "conn", os.Getenv("FAULTTEST_CONN_STR"), "PostgreSQL connection string (used for injection)")
	fs.StringVar(&cfg.ReplicaConnStr, "replica-conn", os.Getenv("FAULTTEST_REPLICA_CONN_STR"), "Replica PostgreSQL connection string")
	fs.StringVar(&cfg.AgentConnStr, "agent-conn", os.Getenv("FAULTTEST_AGENT_CONN_STR"), "Connection string or alias sent to the agent in prompts (defaults to --conn)")
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
	fs.BoolVar(&cfg.AutoDB, "auto-db", false, "Spin up a temporary Docker PostgreSQL for injection; implies --external, no BYO database needed")

	// SSH injection backend.
	fs.StringVar(&cfg.SSHHost, "ssh-host", "", "SSH target for ssh_exec faults (user@host or host); triggers ExternalInject mode")
	fs.StringVar(&cfg.SSHUser, "ssh-user", os.Getenv("USER"), "SSH username for ssh_exec faults (prepended to host when no @ in --ssh-host)")
	fs.StringVar(&cfg.SSHKeyPath, "ssh-key", "", "SSH private key path for ssh_exec faults")

	// Stability / repeat mode.
	fs.IntVar(&cfg.Repeat, "repeat", 1, "Run each fault N times (inject→triage→teardown) and print a stability report; remediation is skipped when N > 1")

	// Remediation phase.
	fs.BoolVar(&cfg.RemediateEnabled, "remediate", false, "Run remediation phase after injection+diagnosis")
	fs.StringVar(&cfg.GatewayURL, "gateway", os.Getenv("FAULTTEST_GATEWAY_URL"), "Gateway URL for playbook/agent remediation and vault playbook checks")
	fs.StringVar(&cfg.GatewayAPIKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key for remediation (or HELPDESK_CLIENT_API_KEY)")
	fs.StringVar(&cfg.GatewayPurpose, "purpose", "diagnostic", "Purpose declared in gateway requests (diagnostic, remediation, maintenance, …)")
	fs.StringVar(&cfg.ApprovalMode, "approval-mode", "", "Override playbook approval_mode for this run (auto|session|manual|force). Empty = playbook default. Use 'force' to bypass manual gates in automated runs.")
	fs.StringVar(&cfg.OperatorID, "operator", os.Getenv("HELPDESK_OPERATOR"), "User identity sent as X-User on gateway requests (e.g. alice@example.com). Must have roles required by approval_override_roles to avoid mode clamping.")

	// Policy safety check.
	fs.StringVar(&cfg.InfraConfigPath, "infra-config", "", "Path to infrastructure.json; when set, target must have a 'test' or 'chaos' tag")

	// Customer catalog support.
	var extraCatalogs stringSliceFlag
	fs.Var(&extraCatalogs, "catalog", "Additional customer catalog file (repeatable)")
	fs.StringVar(&cfg.SourceFilter, "source", "", "Filter faults by source: builtin or custom")
	fs.StringVar(&cfg.ReportDir, "report-dir", ".", "Directory to write the JSON report (default: current directory)")
	fs.BoolVar(&cfg.ReportPerFault, "report-per-fault", false, "Write a separate JSON report per fault (faulttest-{runID}-{faultID}.json) in addition to the combined report")

	// Diagnosis model annotation — recorded in stability certs, not used to call any LLM.
	fs.StringVar(&cfg.DiagnosisModel, "agent-model", os.Getenv("HELPDESK_MODEL_NAME"), "Model used by the triage agent (annotation in stability cert; default: HELPDESK_MODEL_NAME)")

	// LLM judge options.
	fs.BoolVar(&cfg.JudgeEnabled, "judge", false, "Enable LLM-as-judge for semantic diagnosis scoring")
	fs.BoolVar(&cfg.RemediationJudgeEnabled, "remediation-judge", false, "Enable LLM-as-judge for remediation approach scoring (requires --remediate)")
	fs.StringVar(&cfg.JudgeModel, "judge-model", "", "Model name for judge (default: HELPDESK_MODEL_NAME env var)")
	fs.StringVar(&cfg.JudgeVendor, "judge-vendor", "", "Model vendor for judge: anthropic or google (default: HELPDESK_MODEL_VENDOR env var)")
	fs.StringVar(&cfg.JudgeAPIKey, "judge-api-key", "", "API key for judge (default: HELPDESK_API_KEY env var)")

	// Audit-based tool evidence.
	fs.StringVar(&cfg.AuditURL, "audit-url", "", "Audit service base URL (e.g. http://localhost:7070); when set, tool evidence uses audit trail")

	// Completion webhook.
	fs.StringVar(&cfg.NotifyURL, "notify-url", "", "Webhook URL for POST notification on run completion (e.g. Slack incoming webhook)")

	// Gateway-routed diagnosis (A/B comparison mode).
	fs.BoolVar(&cfg.ViaGateway, "via-gateway", true, "Route diagnosis through the gateway instead of calling the agent directly (requires --gateway and diagnosis_playbook_series_id in the catalog)")

	// Async gate and step approvals (K8s/Docker/headless safe).
	fs.BoolVar(&cfg.GateEscalation, "gate-escalation", false, "Send gate_escalation=true on playbook run requests so the gateway intercepts ESCALATE_TO at the phase boundary")
	fs.BoolVar(&cfg.EmitAndWait, "emit-and-wait", false, "Poll for gate and step approvals instead of reading from /dev/tty (safe in K8s Jobs and Docker containers)")

	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
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

	// Resolve named infra aliases to actual DSNs so downstream code always gets
	// a real connection string and agents don't receive an ambiguous alias.
	// Log the alias→host mapping immediately so operators can verify the right
	// targets were chosen before any test run begins.
	origConn := cfg.ConnStr
	origAgentConn := cfg.AgentConnStr
	origReplicaConn := cfg.ReplicaConnStr
	cfg.ConnStr = resolveConnAlias(cfg.InfraConfigPath, cfg.ConnStr)
	cfg.ReplicaConnStr = resolveConnAlias(cfg.InfraConfigPath, cfg.ReplicaConnStr)
	cfg.AgentConnStr = resolveConnAlias(cfg.InfraConfigPath, cfg.AgentConnStr)
	logConnResolution("--conn", origConn, cfg.ConnStr)
	logConnResolution("--replica-conn", origReplicaConn, cfg.ReplicaConnStr)
	logConnResolution("--agent-conn", origAgentConn, cfg.AgentConnStr)

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

	fmt.Printf("%-30s %-12s %-10s %-8s %-7s %-8s %s\n", "ID", "CATEGORY", "SEVERITY", "EXTERNAL", "DB", "SOURCE", "NAME")
	fmt.Println(strings.Repeat("-", 107))
	for _, f := range failures {
		ext := ""
		if f.ExternalCompat {
			ext = "yes"
		}
		db := "-"
		if f.IsAutoDBCompat() {
			db = "auto"
		} else if f.ExternalCompat {
			db = "byo"
		}
		fmt.Printf("%-30s %-12s %-10s %-8s %-7s %-8s %s\n", f.ID, f.Category, f.Severity, ext, db, f.Source, f.Name)
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

	// Auto-DB: spin up a temporary PostgreSQL container.
	if cfg.AutoDB {
		cfg.External = true
		fmt.Printf("Starting temporary PostgreSQL container (%s)...\n", autoDBImage)
		connStr, teardown, err := startAutoDBContainer(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: --auto-db: %v\n", err)
			os.Exit(1)
		}
		defer teardown()
		cfg.ConnStr = connStr
		fmt.Printf("Auto-DB ready: %s\n\n", connStr)
		if cfg.GatewayURL != "" {
			if err := registerAutoDBWithGateway(cfg.GatewayURL, cfg.GatewayAPIKey, connStr); err != nil {
				slog.Warn("could not register auto-DB with gateway — DB agent may reject connection", "err", err)
			}
		}
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

	// Initialize LLM judge completer if either judge mode is enabled.
	var judgeCompleter faultlib.TextCompleter
	var judgeModel string
	if cfg.JudgeEnabled || cfg.RemediationJudgeEnabled {
		var err error
		judgeCompleter, err = newJudgeCompleter(ctx, cfg)
		if err != nil {
			slog.Warn("LLM judge disabled: could not initialize", "err", err)
		} else {
			judgeModel = cfg.JudgeModel
			if judgeModel == "" {
				judgeModel = os.Getenv("HELPDESK_MODEL_NAME")
			}
			if cfg.JudgeEnabled {
				slog.Info("LLM diagnosis judge enabled", "vendor", cfg.JudgeVendor, "model", judgeModel)
			}
			if cfg.RemediationJudgeEnabled {
				slog.Info("LLM remediation judge enabled", "vendor", cfg.JudgeVendor, "model", judgeModel)
			}
		}
	}

	runID := uuid.New().String()[:8]
	var results []EvalResult

	for _, f := range failures {
		nReps := cfg.Repeat
		if nReps < 1 {
			nReps = 1
		}
		repeatMode := nReps > 1

		if repeatMode {
			fmt.Printf("\n--- Testing: %s (%s) — %d runs ---\n", f.Name, f.ID, nReps)
		} else {
			fmt.Printf("\n--- Testing: %s (%s) ---\n", f.Name, f.ID)
		}

		// Skip faults that require a replica when none is configured.
		if faultNeedsReplica(f) && cfg.ReplicaConnStr == "" {
			slog.Warn("skipping fault: requires --replica-conn", "id", f.ID)
			fmt.Printf("Result: [SKIP] replica connection not configured (pass --replica-conn)\n")
			continue
		}

		if repeatMode && cfg.RemediateEnabled {
			slog.Warn("--remediate is disabled in --repeat mode", "repeat", nReps)
		}

		var repResults []EvalResult

		for rep := range nReps {
			// Save original conn string for config-override failures; restore after each rep.
			origConn := cfg.ConnStr

			if repeatMode {
				fmt.Printf("\n  Run %d/%d\n", rep+1, nReps)
			}

			// Per-rep trace ID. Single-run format stays unchanged; repeat runs append -rN
			// so each rep gets its own audit window.
			faultTraceID := "faulttest-" + runID + "-" + f.ID
			if repeatMode {
				faultTraceID = fmt.Sprintf("faulttest-%s-%s-r%d", runID, f.ID, rep+1)
			}
			faultCtx := context.WithValue(ctx, ctxKeyFaultTraceID{}, faultTraceID)

			// 1. Inject.
			if err := injector.Inject(ctx, f); err != nil {
				slog.Error("injection failed", "id", f.ID, "rep", rep+1, "err", err)
				repResults = append(repResults, EvalResult{
					FailureID:   f.ID,
					FailureName: f.Name,
					Category:    f.Category,
					Error:       fmt.Sprintf("injection failed: %v", err),
				})
				cfg.ConnStr = origConn
				break // abort remaining reps for this fault
			}

			// 2. Run agent (record call start for audit window).
			callStart := time.Now()
			resp := runner.Run(faultCtx, f)

			if resp.CrystalBall {
				slog.Warn("⚠  crystal-ball mode active on gateway — playbook scaffolding is bypassed; this result measures unguided LLM capability only")
			}

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
				evalResult.CrystalBall = resp.CrystalBall
				evalResult.RunID = resp.RunID

				// Detect protocol violations from gateway warnings.
				// When the fallback gate fires (agent omitted TRANSITION_TO/ESCALATE_TO),
				// the gateway appends a warning. Cap the score to signal the breach.
				if len(resp.Warnings) > 0 {
					evalResult.GatewayWarnings = resp.Warnings
					for _, w := range resp.Warnings {
						if strings.Contains(w, "omitted required TRANSITION_TO") ||
							strings.Contains(w, "ESCALATE_TO signal") {
							evalResult.ProtocolViolation = true
							break
						}
					}
				}
				if evalResult.ProtocolViolation {
					const protocolViolationCap = 0.75
					if evalResult.Score > protocolViolationCap {
						evalResult.Score = protocolViolationCap
						evalResult.Passed = evalResult.Score >= 0.6 && evalResult.KeywordPass
					}
				}

				// Push judge reasoning to the audit store so it appears alongside
				// live agent_reasoning events in the governance trail.
				if !evalResult.JudgeSkipped && evalResult.JudgeReasoning != "" {
					faultTraceID := "ft_" + runID + "_" + f.ID
					pushJudgeReasoning(ctx, cfg.AuditURL, cfg.GatewayAPIKey, faultTraceID,
						agentNameFromCategory(f.Category), evalResult.JudgeReasoning, auditTools)
				}
			}

			// 4. Remediation phase (skipped in repeat mode).
			// When gate_escalation=true, the triage playbook may return pending_gate at
			// the triage→remediation boundary. In that case, the gate handler drives
			// operator approval and recovery instead of the normal Remediate path.
			runRemediationJudge := func(remResult RemediationResult) {
				if !cfg.RemediationJudgeEnabled || judgeCompleter == nil || remResult.RunID == "" {
					return
				}
				steps := fetchRemediationSteps(faultCtx, cfg.GatewayURL, cfg.GatewayAPIKey, remResult.RunID)
				jr := faultlib.JudgeRemediation(faultCtx, faultlib.RemediationJudgeInput{
					FaultName:        f.Name,
					FaultDescription: f.Description,
					PlaybookGuidance: f.Remediation.AgentPrompt,
					Steps:            steps,
					RecoveryTimeSecs: remResult.RecoveryTimeSecs,
					Passed:           remResult.Passed,
				}, judgeCompleter, judgeModel)
				evalResult.RemediationJudgeScore = jr.Score
				evalResult.RemediationJudgeReasoning = jr.Reasoning
				evalResult.RemediationJudgeSkipped = jr.Skipped
				if !jr.Skipped {
					fmt.Printf("Remediation Judge:   score=%d%% — %s\n", int(jr.Score*100), jr.Reasoning)
				}
			}

			if !repeatMode && cfg.RemediateEnabled && resp.Status == "pending_gate" {
				remResult := remediator.HandlePendingGate(faultCtx, f, resp)
				evalResult.RemediationAttempted = true
				evalResult.RemediationPassed = remResult.Passed
				evalResult.RecoveryTimeSecs = remResult.RecoveryTimeSecs
				evalResult.RemediationScore = remResult.Score
				evalResult.RemediationMethod = remResult.Method
				if remResult.Err != nil {
					evalResult.RemediationError = remResult.Err.Error()
				}
				evalResult.OverallScore = evalResult.Score*0.6 + remResult.Score*0.4
				runRemediationJudge(remResult)
				if remResult.Passed {
					fmt.Printf("Remediation: RECOVERED in %.1fs (score: %.0f%%)\n", remResult.RecoveryTimeSecs, remResult.Score*100)
					printIncidentSummary(resp, remResult.RecoveryTimeSecs, cfg.GatewayURL)
					if cfg.GatewayURL != "" {
						if pbID, vaultErr := requestVaultDraft(faultCtx, cfg, faultTraceID, "resolved", f.DiagnosisPlaybookSeriesID); vaultErr != nil {
							slog.Warn("vault: could not generate playbook draft", "fault", f.ID, "err", vaultErr)
						} else if pbID != "" {
							fmt.Printf("Vault: draft saved → %s (activate with 'faulttest vault list')\n", pbID)
						}
					}
				} else {
					fmt.Printf("Remediation: FAILED — %v\n", remResult.Err)
				}
			} else if !repeatMode && cfg.RemediateEnabled && resp.ChainedRunID == "" && (f.Remediation.PlaybookID != "" || f.Remediation.AgentPrompt != "") {
				remResult := remediator.Remediate(faultCtx, f, resp.RunID)
				evalResult.RemediationAttempted = true
				evalResult.RemediationPassed = remResult.Passed
				evalResult.RecoveryTimeSecs = remResult.RecoveryTimeSecs
				evalResult.RemediationScore = remResult.Score
				evalResult.RemediationMethod = remResult.Method
				if remResult.Err != nil {
					evalResult.RemediationError = remResult.Err.Error()
				}
				evalResult.OverallScore = evalResult.Score*0.6 + remResult.Score*0.4
				runRemediationJudge(remResult)
				if remResult.Passed {
					fmt.Printf("Remediation: RECOVERED in %.1fs (score: %.0f%%)\n", remResult.RecoveryTimeSecs, remResult.Score*100)
					if cfg.GatewayURL != "" {
						if pbID, vaultErr := requestVaultDraft(faultCtx, cfg, faultTraceID, "resolved", f.DiagnosisPlaybookSeriesID); vaultErr != nil {
							slog.Warn("vault: could not generate playbook draft", "fault", f.ID, "err", vaultErr)
						} else if pbID != "" {
							fmt.Printf("Vault: draft saved → %s (activate with 'faulttest vault list')\n", pbID)
						}
					}
				} else {
					fmt.Printf("Remediation: FAILED — %v\n", remResult.Err)
				}
				if cfg.ApprovalMode != "force" {
					remediator.submitFeedback(faultCtx, resp.RunID, remResult.RunID, resp.DiagnosticReport)
				}
			} else {
				evalResult.OverallScore = evalResult.Score
			}

			// In force mode with judge enabled, auto-submit triage feedback derived
			// from the judge score so vault calibration accumulates data without
			// operator input. Feedback is tagged "auto_judge" so calibration can
			// distinguish it from human verdicts and display the appropriate note.
			if cfg.ApprovalMode == "force" && !evalResult.JudgeSkipped && resp.RunID != "" && cfg.GatewayURL != "" {
				correct := evalResult.Score >= 0.8
				remediator.postFeedback(faultCtx, resp.RunID, "triage", "post_incident", &correct, evalResult.JudgeReasoning, "auto_judge")
			}

			repResults = append(repResults, evalResult)

			// 5. Teardown.
			cfg.ConnStr = origConn
			if err := injector.Teardown(ctx, f); err != nil {
				slog.Error("teardown failed", "id", f.ID, "rep", rep+1, "err", err)
			}

			// Print result: compact one-liner in repeat mode, full detail otherwise.
			status := "PASS"
			if !evalResult.Passed {
				status = "FAIL"
			}
			if repeatMode {
				violation := ""
				if evalResult.ProtocolViolation {
					violation = " protocol-violation"
				}
				fmt.Printf("  [%s] score=%d%%%s\n", status, int(evalResult.Score*100), violation)
				for _, h := range evalResult.Hypotheses {
					pct := int(h.Confidence * 100)
					var tag string
					switch {
					case h.IsPrimary:
						tag = fmt.Sprintf("[PRIMARY %d%%]", pct)
					case h.RejectedReason != "":
						tag = fmt.Sprintf("[REJECTED %d%%]", pct)
					default:
						tag = fmt.Sprintf("[%d%%]", pct)
					}
					if h.RejectedReason != "" {
						fmt.Printf("         %s %s — %s\n", tag, h.Text, h.RejectedReason)
					} else {
						fmt.Printf("         %s %s\n", tag, h.Text)
					}
				}
			} else {
				diagLabel := "category"
				if !evalResult.JudgeSkipped {
					diagLabel = "judge"
				}
				fmt.Printf("Diagnostic Result:   [%s] score=%d%% (keywords=%d%% tools=%d%% %s=%d%%)\n",
					status, int(evalResult.Score*100),
					int(evalResult.KeywordScore*100), int(evalResult.ToolScore*100), diagLabel, int(evalResult.DiagnosisScore*100))
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
		} // end rep loop

		results = append(results, repResults...)

		if repeatMode {
			sr := buildStabilityReport(f, repResults)
			sr.Print()
			if cfg.DiagnosisModel == "" {
				slog.Warn("stability cert not posted: diagnosis model unknown — set HELPDESK_MODEL_NAME or --agent-model so the cert is attributed to the right model")
			} else {
				postStabilityCert(ctx, cfg, f, sr)
			}
		}

		if cfg.ReportPerFault {
			faultReport := BuildReport(runID+"-"+f.ID, repResults)
			faultReportFile := fmt.Sprintf("%s/faulttest-%s-%s.json", cfg.ReportDir, runID, f.ID)
			if err := faultReport.WriteJSON(faultReportFile); err != nil {
				slog.Error("failed to write per-fault report", "fault", f.ID, "err", err)
			} else {
				fmt.Printf("Fault report: %s\n", faultReportFile)
			}
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
	report.PrintJSON()

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
	// When --conn is a named infrastructure alias (e.g. "alloydb-on-vm") rather
	// than a DSN, connStrHost returns "". Fall back to the alias itself so the
	// target column in vault status is always populated.
	if target == "" {
		target = cfg.ConnStr
	}
	if err := appendHistory(report, target); err != nil {
		slog.Warn("failed to append to run history", "err", err, "path", historyFilePath())
	}

	// Post evaluation scores to auditd (via gateway) so they can be joined
	// with operator feedback for calibration. Failures are non-fatal.
	if cfg.GatewayURL != "" {
		postEvaluations(cfg.GatewayURL, cfg.GatewayAPIKey, report.Results)
	}
}

// postNotify POSTs the full JSON report to the given webhook URL. Failures are
// logged at Warn level and never cause the faulttest run to fail.
// registerAutoDBWithGateway calls POST /api/v1/admin/infra/register-db on the gateway
// so that both the gateway and the DB agent can resolve the auto-created connection string.
// The entry gets "chaos" and "test" tags so governance policy allows it.
func registerAutoDBWithGateway(gatewayURL, apiKey, connStr string) error {
	// Derive a stable server_id from the port in the connection string.
	serverID := "faulttest-auto"
	for _, part := range strings.Fields(connStr) {
		if strings.HasPrefix(part, "port=") {
			serverID = "faulttest-auto-" + strings.TrimPrefix(part, "port=")
			break
		}
	}
	body, err := json.Marshal(map[string]any{
		"server_id":         serverID,
		"name":              "Auto-DB faulttest instance",
		"connection_string": connStr,
		"tags":              []string{"chaos", "test"},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/api/v1/admin/infra/register-db", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned %d", resp.StatusCode)
	}
	slog.Info("auto-DB registered with gateway", "server_id", serverID)
	return nil
}

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
// draft. seriesID pins the draft to an existing series so the handler can fetch
// the active version and use improvement mode instead of cold synthesis.
// Returns the persisted playbook_id, or "" when auditd is not configured.
func requestVaultDraft(ctx context.Context, cfg *HarnessConfig, traceID, outcome, seriesID string) (string, error) {
	body := map[string]string{
		"trace_id": traceID,
		"outcome":  outcome,
	}
	if seriesID != "" {
		body["series_id"] = seriesID
	}
	reqBody, err := json.Marshal(body)
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
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned %d: %s", resp.StatusCode, respBody)
	}
	var result struct {
		PlaybookID string `json:"playbook_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.PlaybookID, nil
}

// fetchRemediationSteps retrieves the executed steps for a remediation playbook
// run from the gateway and converts them to faultlib.RemediationStep for the
// remediation judge. Returns nil when the gateway is unreachable or no steps
// are recorded.
func fetchRemediationSteps(ctx context.Context, gatewayURL, apiKey, runID string) []faultlib.RemediationStep {
	if gatewayURL == "" || runID == "" {
		return nil
	}
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbook-runs/" + runID + "/steps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var raw []struct {
		Tool   string         `json:"tool"`
		Args   map[string]any `json:"args"`
		Status string         `json:"status"`
		Result string         `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}
	steps := make([]faultlib.RemediationStep, 0, len(raw))
	for _, s := range raw {
		steps = append(steps, faultlib.RemediationStep{
			Tool:   s.Tool,
			Args:   s.Args,
			Status: s.Status,
			Result: s.Result,
		})
	}
	return steps
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
func validatePlaybookExists(gatewayURL, apiKey, seriesID string) bool {
	return checkPlaybook(gatewayURL, apiKey, seriesID) == playbookFound
}

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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/fleet"
	"helpdesk/internal/infra"
	"helpdesk/internal/logging"
)

func main() {
	remaining := logging.InitLogging(os.Args[1:])

	var (
		jobFile              = flag.String("job-file", envOrDefault("HELPDESK_FLEET_JOB_FILE", ""), "Path to JSON job definition file")
		gatewayURL           = flag.String("gateway", envOrDefault("HELPDESK_GATEWAY_URL", "http://localhost:8080"), "Gateway URL")
		auditURL             = flag.String("audit-url", envOrDefault("HELPDESK_AUDIT_URL", "http://localhost:1199"), "Auditd URL for job tracking")
		apiKey               = flag.String("api-key", envOrDefault("HELPDESK_CLIENT_API_KEY", ""), "Service account API key")
		auditAPIKey          = flag.String("audit-api-key", envOrDefault("HELPDESK_AUDIT_API_KEY", ""), "Bearer token for direct auditd calls")
		infraPath            = flag.String("infra", envOrDefault("HELPDESK_INFRA_CONFIG", "infrastructure.json"), "Path to infrastructure.json")
		dryRun               = flag.Bool("dry-run", false, "Print plan without contacting gateway or auditd")
		canaryOverride       = flag.Int("canary", 0, "Override strategy.canary_count")
		waveSizeOverride     = flag.Int("wave-size", 0, "Override strategy.wave_size")
		pauseOverride        = flag.Int("pause", -1, "Override strategy.wave_pause_seconds")
		approvalPollInterval = flag.Duration("approval-poll-interval", 5*time.Second, "How often to poll for approval status")
		schemaDriftFlag      = flag.String("schema-drift", envOrDefault("HELPDESK_SCHEMA_DRIFT", ""), `Schema drift policy: "abort" (default), "warn", or "ignore"`)
		refreshSnapshots     = flag.Bool("refresh-snapshots", false, "Refresh tool_snapshots from live registry, write back to job file, and exit")
		planDescription      = flag.String("plan-description", "", "Call the fleet planner with this description instead of loading --job-file")
		planTargetHints      = flag.String("plan-target-hints", "", "Comma-separated target hints passed to the planner with --plan-description")
	)
	var replanFlag replanMode
	flag.CommandLine.Var(&replanFlag, "replan", `On schema drift, replan from stored plan_description.\n--replan writes fresh plan to --job-file and stops; --replan=auto also re-executes`)
	flag.CommandLine.Parse(remaining) //nolint:errcheck

	if *jobFile == "" && *planDescription == "" {
		slog.Error("--job-file or --plan-description is required")
		flag.Usage()
		os.Exit(1)
	}

	// Load or generate the job definition.
	var def fleet.JobDef
	if *planDescription != "" {
		var hints []string
		if *planTargetHints != "" {
			for _, h := range strings.Split(*planTargetHints, ",") {
				if h = strings.TrimSpace(h); h != "" {
					hints = append(hints, h)
				}
			}
		}
		planned, err := callFleetPlan(*gatewayURL, *apiKey, *planDescription, hints)
		if err != nil {
			slog.Error("--plan-description: planner call failed", "err", err)
			os.Exit(1)
		}
		def = *planned
		slog.Info("plan generated from description", "name", def.Name, "steps", len(def.Change.Steps))
		if *jobFile != "" {
			if out, err := json.MarshalIndent(def, "", "  "); err == nil {
				if werr := os.WriteFile(*jobFile, out, 0o644); werr != nil {
					slog.Warn("--plan-description: could not save plan to job file", "path", *jobFile, "err", werr)
				} else {
					slog.Info("plan saved", "path", *jobFile)
				}
			}
		}
	} else {
		defData, err := os.ReadFile(*jobFile)
		if err != nil {
			slog.Error("failed to read job file", "path", *jobFile, "err", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(defData, &def); err != nil {
			slog.Error("failed to parse job file", "err", err)
			os.Exit(1)
		}
	}

	// Refresh tool_snapshots from the live registry and write back to the job file.
	if *refreshSnapshots {
		updated, err := callFleetSnapshot(*gatewayURL, *apiKey, &def)
		if err != nil {
			slog.Error("--refresh-snapshots failed", "err", err)
			os.Exit(1)
		}
		out, err := json.MarshalIndent(updated, "", "  ")
		if err != nil {
			slog.Error("--refresh-snapshots: marshal failed", "err", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*jobFile, out, 0o644); err != nil {
			slog.Error("--refresh-snapshots: write failed", "path", *jobFile, "err", err)
			os.Exit(1)
		}
		slog.Info("snapshots refreshed", "path", *jobFile, "tools", len(updated.ToolSnapshots))
		return
	}

	// Apply strategy overrides from flags.
	def.Strategy.Defaults()
	if *dryRun {
		def.Strategy.DryRun = true
	}
	if *canaryOverride > 0 {
		def.Strategy.CanaryCount = *canaryOverride
	}
	if *waveSizeOverride > 0 {
		def.Strategy.WaveSize = *waveSizeOverride
	}
	if *pauseOverride >= 0 {
		def.Strategy.WavePauseSeconds = *pauseOverride
	}

	// Resolve schema drift policy. Priority: job file > --schema-drift flag > env var > "abort".
	driftPolicy := "abort"
	if *schemaDriftFlag != "" {
		driftPolicy = *schemaDriftFlag
	}
	if def.Strategy.SchemaDrift != "" {
		driftPolicy = def.Strategy.SchemaDrift // job file wins
	}

	// Validate required fields.
	if def.Name == "" {
		slog.Error("job definition missing required field: name")
		os.Exit(1)
	}
	if len(def.Change.Steps) == 0 {
		slog.Error("job definition has no change steps (steps required)")
		os.Exit(1)
	}

	// Load infrastructure config.
	infraCfg, err := infra.Load(*infraPath)
	if err != nil {
		slog.Error("failed to load infrastructure config", "path", *infraPath, "err", err)
		os.Exit(1)
	}

	// Resolve target servers.
	servers, err := resolveTargets(infraCfg, def.Targets)
	if err != nil {
		slog.Error("failed to resolve targets", "err", err)
		os.Exit(1)
	}
	if len(servers) == 0 {
		slog.Error("no servers matched the target specification", "tags", def.Targets.Tags, "names", def.Targets.Names)
		os.Exit(1)
	}

	// Dry-run: print plan (including drift report) and exit.
	if def.Strategy.DryRun {
		printDryRunPlan(&def, servers)
		// Always show drift results in dry-run regardless of policy.
		liveTools, err := fetchLiveTools(*gatewayURL)
		if err != nil {
			fmt.Printf("\nSCHEMA DRIFT: could not fetch live registry: %v\n", err)
		} else {
			results, _ := CheckSchemaDrift(def.ToolSnapshots, liveTools, "warn")
			printDriftReport(results, driftPolicy)
		}
		return
	}

	ctx := context.Background()

	// Schema drift check: compare job snapshots against the live registry.
	// Must run before preflight so we abort before contacting any server.
	if driftPolicy != "ignore" {
		liveTools, err := fetchLiveTools(*gatewayURL)
		if err != nil {
			slog.Error("schema drift: failed to fetch live tool registry — aborting", "err", err)
			os.Exit(1)
		}
		_, driftErr := CheckSchemaDrift(def.ToolSnapshots, liveTools, driftPolicy)
		if driftErr != nil {
			if replanFlag.isSet() && def.PlanDescription != "" {
				slog.Warn("schema drift detected; replanning", "description", def.PlanDescription, "mode", replanFlag.mode)
				planned, perr := callFleetPlan(*gatewayURL, *apiKey, def.PlanDescription, def.PlanTargetHints)
				if perr != nil {
					slog.Error("--replan: planner call failed", "err", perr)
					os.Exit(1)
				}

				// Check divergence between original and fresh plan.
				original := def
				div := checkPlanDivergence(&original, planned)
				if div.Significant() {
					if replanFlag.mode == "auto" {
						slog.Error("--replan=auto: replanned plan diverges significantly from original — review before executing",
							"divergence", div.String())
						slog.Error("hint: re-run with --replan (without =auto) to write the fresh plan to --job-file for review")
						os.Exit(1)
					}
					slog.Warn("--replan: replanned plan diverges from original", "divergence", div.String())
				}

				// In stop mode: write fresh plan to --job-file and exit.
				if replanFlag.mode == "stop" {
					out, merr := json.MarshalIndent(planned, "", "  ")
					if merr != nil {
						slog.Error("--replan: failed to marshal fresh plan", "err", merr)
						os.Exit(1)
					}
					dest := *jobFile
					if dest == "" {
						fmt.Println(string(out))
						slog.Info("--replan: fresh plan written to stdout; review and re-run with --job-file")
					} else {
						if werr := os.WriteFile(dest, out, 0o644); werr != nil {
							slog.Error("--replan: failed to write fresh plan", "path", dest, "err", werr)
							os.Exit(1)
						}
						slog.Info("--replan: fresh plan written — review and re-run without --replan", "path", dest)
					}
					os.Exit(0)
				}

				// auto mode: apply overrides and continue.
				def = *planned
				def.Strategy.Defaults()
				if *dryRun {
					def.Strategy.DryRun = true
				}
				if *canaryOverride > 0 {
					def.Strategy.CanaryCount = *canaryOverride
				}
				if *waveSizeOverride > 0 {
					def.Strategy.WaveSize = *waveSizeOverride
				}
				if *pauseOverride >= 0 {
					def.Strategy.WavePauseSeconds = *pauseOverride
				}
				// Re-resolve servers from new plan targets.
				servers, err = resolveTargets(infraCfg, def.Targets)
				if err != nil {
					slog.Error("--replan: failed to resolve targets", "err", err)
					os.Exit(1)
				}
				if len(servers) == 0 {
					slog.Error("--replan: no servers matched new plan targets",
						"tags", def.Targets.Tags, "names", def.Targets.Names)
					os.Exit(1)
				}
				// Verify the fresh plan itself has no drift.
				if _, err2 := CheckSchemaDrift(def.ToolSnapshots, liveTools, driftPolicy); err2 != nil {
					slog.Error("schema drift check failed after replan", "err", err2)
					os.Exit(1)
				}
				slog.Info("replan succeeded", "name", def.Name, "steps", len(def.Change.Steps))
			} else {
				slog.Error("schema drift check failed", "err", driftErr)
				os.Exit(1)
			}
		}
	}

	// Preflight checks: verify all servers are reachable before creating the job.
	pfCfg := preflightConfig{
		gatewayURL: *gatewayURL,
		apiKey:     *apiKey,
	}
	failures := runPreflight(ctx, pfCfg, servers)
	if len(failures) > 0 {
		var failed []string
		for srv, err := range failures {
			failed = append(failed, fmt.Sprintf("%s: %v", srv, err))
		}
		slog.Error("preflight checks failed — aborting", "failures", strings.Join(failed, "; "))
		os.Exit(1)
	}

	// Build stage assignments for job record.
	assignments := buildStageAssignments(servers, def.Strategy)

	// Determine submittedBy: use the user from env or a default for service accounts.
	submittedBy := envOrDefault("HELPDESK_CLIENT_USER", "fleet-runner")

	// Create the job record via the gateway (records journey anchor event).
	jobID, err := submitJob(ctx, *gatewayURL, *auditURL, *apiKey, *auditAPIKey, submittedBy, &def, servers, assignments)
	if err != nil {
		slog.Error("failed to create fleet job record", "err", err)
		// Non-fatal: continue without job tracking.
		jobID = ""
	} else {
		slog.Info("fleet job created", "job_id", jobID)
	}

	// Execute the staged rollout.
	rcfg := runnerConfig{
		gatewayURL:           *gatewayURL,
		auditURL:             *auditURL,
		apiKey:               *apiKey,
		auditAPIKey:          *auditAPIKey,
		jobID:                jobID,
		submittedBy:          submittedBy,
		approvalPollInterval: *approvalPollInterval,
	}

	runErr := runStages(ctx, rcfg, &def, servers)

	// Finalize job record.
	status := "completed"
	summary := fmt.Sprintf("Applied %d step(s) to %d server(s).", len(def.Change.Steps), len(servers))
	if runErr != nil {
		status = "failed"
		summary = fmt.Sprintf("Failed: %v", runErr)
	}
	if jobID != "" {
		if err := finalizeJob(ctx, *auditURL, *auditAPIKey, jobID, status, summary); err != nil {
			slog.Warn("failed to finalize fleet job", "err", err)
		}
	}

	if runErr != nil {
		slog.Error("fleet job failed", "job_id", jobID, "err", runErr)
		os.Exit(1)
	}

	slog.Info("fleet job completed", "job_id", jobID, "servers", len(servers))
}

// printDryRunPlan prints the resolved server list and stage plan without contacting any service.
func printDryRunPlan(def *fleet.JobDef, servers []string) {
	fmt.Printf("DRY RUN — fleet job: %s\n", def.Name)
	fmt.Printf("Steps (%d):\n", len(def.Change.Steps))
	for i, step := range def.Change.Steps {
		onFail := step.OnFailure
		if onFail == "" {
			onFail = "stop"
		}
		fmt.Printf("  [%d] %s/%s  (on_failure=%s)\n", i+1, step.Agent, step.Tool, onFail)
	}

	// Show approval requirement if any step is write or destructive.
	actionClass := jobActionClass(def.Change.Steps)
	if actionClass == audit.ActionWrite || actionClass == audit.ActionDestructive {
		fmt.Printf("\nAPPROVAL WOULD BE REQUIRED: job contains %s operations\n", actionClass)
	}

	fmt.Printf("Resolved servers (%d):\n", len(servers))

	assignments := buildStageAssignments(servers, def.Strategy)
	for _, a := range assignments {
		fmt.Printf("  %-40s  [%s]\n", a.server, a.stage)
	}

	strategy := def.Strategy
	fmt.Printf("\nStrategy:\n")
	fmt.Printf("  canary_count:        %d\n", strategy.CanaryCount)
	fmt.Printf("  wave_size:           %d", strategy.WaveSize)
	if strategy.WaveSize == 0 {
		fmt.Printf(" (all remaining in one wave)")
	}
	fmt.Println()
	fmt.Printf("  wave_pause_seconds:  %d\n", strategy.WavePauseSeconds)
	fmt.Printf("  failure_threshold:   %.0f%%\n", strategy.FailureThreshold*100)
	fmt.Printf("\nNo gateway or auditd contact (dry run).\n")
}

// printDriftReport prints a human-readable schema drift table to stdout.
// Called during dry-run regardless of policy so operators always see drift.
func printDriftReport(results []SchemaDriftResult, policy string) {
	if len(results) == 0 {
		fmt.Printf("\nSCHEMA DRIFT: no drift detected (policy=%s)\n", policy)
		return
	}
	fmt.Printf("\nSCHEMA DRIFT (policy=%s):\n", policy)
	for _, r := range results {
		status := "VERSION CHANGED"
		if r.FingerprintChanged {
			status = "FINGERPRINT CHANGED"
		}
		fmt.Printf("  %-40s  [%s]\n", r.Tool, status)
		fmt.Printf("    planned:  fingerprint=%-12s  version=%s  captured=%s\n",
			r.Snapshot.SchemaFingerprint, r.Snapshot.AgentVersion,
			r.Snapshot.CapturedAt.Format("2006-01-02T15:04:05Z"))
		fmt.Printf("    current:  fingerprint=%-12s  version=%s\n",
			r.CurrentFingerprint, r.CurrentVersion)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

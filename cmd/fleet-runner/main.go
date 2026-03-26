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
		infraPath            = flag.String("infra", envOrDefault("HELPDESK_INFRA_CONFIG", "infrastructure.json"), "Path to infrastructure.json")
		dryRun               = flag.Bool("dry-run", false, "Print plan without contacting gateway or auditd")
		canaryOverride       = flag.Int("canary", 0, "Override strategy.canary_count")
		waveSizeOverride     = flag.Int("wave-size", 0, "Override strategy.wave_size")
		pauseOverride        = flag.Int("pause", -1, "Override strategy.wave_pause_seconds")
		approvalPollInterval = flag.Duration("approval-poll-interval", 5*time.Second, "How often to poll for approval status")
		schemaDriftFlag      = flag.String("schema-drift", envOrDefault("HELPDESK_SCHEMA_DRIFT", ""), `Schema drift policy: "abort" (default), "warn", or "ignore"`)
	)
	flag.CommandLine.Parse(remaining) //nolint:errcheck

	if *jobFile == "" {
		slog.Error("--job-file is required")
		flag.Usage()
		os.Exit(1)
	}

	// Load job definition.
	defData, err := os.ReadFile(*jobFile)
	if err != nil {
		slog.Error("failed to read job file", "path", *jobFile, "err", err)
		os.Exit(1)
	}
	var def fleet.JobDef
	if err := json.Unmarshal(defData, &def); err != nil {
		slog.Error("failed to parse job file", "err", err)
		os.Exit(1)
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
		if _, err := CheckSchemaDrift(def.ToolSnapshots, liveTools, driftPolicy); err != nil {
			slog.Error("schema drift check failed", "err", err)
			os.Exit(1)
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
	jobID, err := submitJob(ctx, *gatewayURL, *auditURL, *apiKey, submittedBy, &def, servers, assignments)
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
		if err := finalizeJob(ctx, *auditURL, jobID, status, summary); err != nil {
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

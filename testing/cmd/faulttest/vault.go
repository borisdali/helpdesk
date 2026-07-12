package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"helpdesk/testing/faultlib"
)

// ── History file ──────────────────────────────────────────────────────────

// historyRun is one faulttest run record stored in the history file.
type historyRun struct {
	RunID     string               `json:"run_id"`
	Timestamp string               `json:"timestamp"`
	// Target identifies the database server that was tested — the --agent-conn
	// alias (e.g. "alloydb-on-vm") when set, otherwise the hostname extracted
	// from --conn. Allows vault commands to filter by deployment environment.
	Target       string               `json:"target,omitempty"`
	Total        int                  `json:"total"`
	Passed       int                  `json:"passed"`
	JudgeEnabled bool                 `json:"judge_enabled,omitempty"`
	Results      []historyFaultResult `json:"results"`
}

// historyFaultResult holds the outcome of one fault within a history run.
type historyFaultResult struct {
	FailureID        string  `json:"failure_id"`
	FailureName      string  `json:"failure_name"`
	Passed           bool    `json:"passed"`
	Score            float64 `json:"score"`             // composite (keyword+tool+category/judge)
	KeywordScore     float64 `json:"keyword_score,omitempty"`
	ToolScore        float64 `json:"tool_score,omitempty"`
	DiagnosisScore   float64 `json:"diagnosis_score,omitempty"` // category match OR judge score
	JudgeUsed        bool    `json:"judge_used,omitempty"`      // true = DiagnosisScore is judge score
	RemediationScore float64 `json:"remediation_score,omitempty"`
	OverallScore     float64 `json:"overall_score,omitempty"`
}

// historyFilePath returns the path for the faulttest history file.
// Overridden by HELPDESK_FAULT_HISTORY_FILE env var.
func historyFilePath() string {
	if p := os.Getenv("HELPDESK_FAULT_HISTORY_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".faulttest", "history.json")
	}
	return filepath.Join(home, ".faulttest", "history.json")
}

// appendHistory appends a run summary to the history file, creating it if needed.
// target is the database server identifier (agent-conn alias or hostname).
func appendHistory(report Report, target string) error {
	path := historyFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating history dir: %w", err)
	}

	var runs []historyRun
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &runs)
	}

	var faultResults []historyFaultResult
	for _, r := range report.Results {
		faultResults = append(faultResults, historyFaultResult{
			FailureID:        r.FailureID,
			FailureName:      r.FailureName,
			Passed:           r.Passed,
			Score:            r.Score,
			KeywordScore:     r.KeywordScore,
			ToolScore:        r.ToolScore,
			DiagnosisScore:   r.DiagnosisScore,
			JudgeUsed:        !r.JudgeSkipped && r.JudgeModel != "",
			RemediationScore: r.RemediationScore,
			OverallScore:     r.OverallScore,
		})
	}
	judgeEnabled := false
	for _, r := range faultResults {
		if r.JudgeUsed {
			judgeEnabled = true
			break
		}
	}
	runs = append(runs, historyRun{
		RunID:        report.ID,
		Timestamp:    report.Timestamp,
		Target:       target,
		Total:        report.Summary.Total,
		Passed:       report.Summary.Passed,
		JudgeEnabled: judgeEnabled,
		Results:      faultResults,
	})

	data, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling history: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// loadHistory loads all runs from the history file.
// Returns (nil, nil) when the file does not exist.
func loadHistory() ([]historyRun, error) {
	path := historyFilePath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}
	var runs []historyRun
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("parsing history: %w", err)
	}
	return runs, nil
}

// ── vault command ─────────────────────────────────────────────────────────

func cmdVault(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault <list|status|drift|accuracy|incidents|journey|versions|calibration|judge-accuracy|cert-compare|suggest|suggest-update|drafts|activate|diff|discard|import>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  list            Show fault↔playbook pairings and last-run status")
		fmt.Fprintln(os.Stderr, "  status          Show pass rate trends from run history")
		fmt.Fprintln(os.Stderr, "  drift           Highlight faults/playbooks with declining pass rates")
		fmt.Fprintln(os.Stderr, "  accuracy        Show diagnosis accuracy for a playbook series")
		fmt.Fprintln(os.Stderr, "  incidents       List incident run IDs for a fault with feedback status")
		fmt.Fprintln(os.Stderr, "  journey         Show audit trail for a trace (tools, decisions, delegations)")
		fmt.Fprintln(os.Stderr, "  versions        Show per-version run stats for a playbook series")
		fmt.Fprintln(os.Stderr, "  calibration     Show how well diagnosis scores predict operator-confirmed accuracy")
		fmt.Fprintln(os.Stderr, "  judge-accuracy  Compare judge predictions (from vault diff) to actual run outcomes")
	fmt.Fprintln(os.Stderr, "  cert-compare    Compare stability certs across two diagnosis models (model upgrade gating)")
		fmt.Fprintln(os.Stderr, "  suggest         Generate a playbook draft from an audit trace")
		fmt.Fprintln(os.Stderr, "  suggest-update  Show proposed update for an existing playbook from a trace")
		fmt.Fprintln(os.Stderr, "  drafts          List inactive (draft) playbooks awaiting activation")
		fmt.Fprintln(os.Stderr, "  diff            Show field-by-field diff between two playbook versions")
		fmt.Fprintln(os.Stderr, "  activate        Promote a draft playbook to active status")
		fmt.Fprintln(os.Stderr, "  discard         Delete a draft playbook")
		fmt.Fprintln(os.Stderr, "  import          Import a local YAML playbook file as a draft (and optionally activate)")
		os.Exit(1)
	}
	// Print gateway identity banner before any subcommand output so operators
	// always know which instance they're talking to (two stacks on the same
	// port look identical without this).
	gwURL := scanFlag(args[1:], "gateway", os.Getenv("FAULTTEST_GATEWAY_URL"))
	gwKey := scanFlag(args[1:], "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"))
	printGatewayBanner(gwURL, gwKey)

	switch args[0] {
	case "list":
		vaultList(args[1:])
	case "status":
		vaultStatus(args[1:])
	case "drift":
		vaultDrift(args[1:])
	case "accuracy":
		vaultAccuracy(args[1:])
	case "incidents":
		vaultIncidents(args[1:])
	case "journey", "journeys":
		vaultJourney(args[1:])
	case "versions":
		vaultVersions(args[1:])
	case "calibration":
		vaultCalibration(args[1:])
	case "judge-accuracy":
		vaultJudgeAccuracy(args[1:])
	case "cert-compare":
		vaultCertCompare(args[1:])
	case "suggest":
		vaultSuggest(args[1:])
	case "suggest-update":
		vaultSuggestUpdate(args[1:])
	case "drafts":
		vaultDrafts(args[1:])
	case "active":
		vaultActive(args[1:])
	case "activate":
		vaultActivate(args[1:])
	case "diff":
		vaultDiff(args[1:])
	case "history":
		vaultHistory(args[1:])
	case "discard":
		vaultDiscard(args[1:])
	case "import":
		vaultImport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown vault subcommand: %q\n", args[0])
		os.Exit(1)
	}
}

// ── vault list ────────────────────────────────────────────────────────────

// playbookGatewayInfo holds live data fetched from the gateway for one playbook series.
type playbookGatewayInfo struct {
	check          playbookCheckResult
	source         string  // "system" | "imported" | "manual" | "generated"
	totalRuns      int
	resolved       int
	resolutionRate float64 // 0.0–1.0
	lastRunAt      string  // RFC3339 or empty
	feedbackCount  int
	correctCount   int
	accuracyRate   float64 // 0.0–1.0; valid only when feedbackCount > 0

	atGateCount              int
	atGateCorrect            int
	atGateAccuracyRate       float64
	postIncidentCount        int
	postIncidentCorrect      int
	postIncidentAccuracyRate float64

	// Remediation feedback fields.
	remediationFeedbackCount       int
	remediationCorrectCount        int
	remediationAccuracyRate        float64
	remediationAtGateCount         int
	remediationAtGateCorrect       int
	remediationPostIncidentCount   int
	remediationPostIncidentCorrect int

	// Efficiency metrics.
	avgStepCount    float64
	avgRecoverySecs float64
}

// fetchPlaybookInfo queries the gateway for a playbook series and returns existence
// status plus inline run stats in a single HTTP call.
func fetchPlaybookInfo(gatewayURL, apiKey, seriesID string) playbookGatewayInfo {
	reqURL := gatewayURL + "/api/v1/fleet/playbooks?series_id=" + seriesID
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return playbookGatewayInfo{check: playbookUnknown}
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return playbookGatewayInfo{check: playbookUnknown}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return playbookGatewayInfo{check: playbookAuthError}
	case http.StatusNotFound:
		return playbookGatewayInfo{check: playbookNotFound}
	case http.StatusOK:
	default:
		return playbookGatewayInfo{check: playbookUnknown}
	}

	var result struct {
		Playbooks []struct {
			Source string `json:"source"`
			Stats  *struct {
				TotalRuns      int     `json:"total_runs"`
				Resolved       int     `json:"resolved"`
				ResolutionRate float64 `json:"resolution_rate"`
				LastRunAt      string  `json:"last_run_at"`
				FeedbackCount  int     `json:"feedback_count"`
				CorrectCount   int     `json:"correct_count"`
				AccuracyRate   float64 `json:"accuracy_rate"`

				AtGateCount              int     `json:"at_gate_count"`
				AtGateCorrect            int     `json:"at_gate_correct"`
				AtGateAccuracyRate       float64 `json:"at_gate_accuracy_rate"`
				PostIncidentCount        int     `json:"post_incident_count"`
				PostIncidentCorrect      int     `json:"post_incident_correct"`
				PostIncidentAccuracyRate float64 `json:"post_incident_accuracy_rate"`

				RemediationFeedbackCount       int     `json:"remediation_feedback_count"`
				RemediationCorrectCount        int     `json:"remediation_correct_count"`
				RemediationAccuracyRate        float64 `json:"remediation_accuracy_rate"`
				RemediationAtGateCount         int     `json:"remediation_at_gate_count"`
				RemediationAtGateCorrect       int     `json:"remediation_at_gate_correct"`
				RemediationPostIncidentCount   int     `json:"remediation_post_incident_count"`
				RemediationPostIncidentCorrect int     `json:"remediation_post_incident_correct"`
				AvgStepCount                  float64 `json:"avg_step_count"`
				AvgRecoverySecs               float64 `json:"avg_recovery_secs"`
			} `json:"stats"`
		} `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Playbooks) == 0 {
		return playbookGatewayInfo{check: playbookNotFound}
	}
	info := playbookGatewayInfo{check: playbookFound, source: result.Playbooks[0].Source}
	if s := result.Playbooks[0].Stats; s != nil {
		info.totalRuns = s.TotalRuns
		info.resolved = s.Resolved
		info.resolutionRate = s.ResolutionRate
		info.lastRunAt = s.LastRunAt
		info.feedbackCount = s.FeedbackCount
		info.correctCount = s.CorrectCount
		info.accuracyRate = s.AccuracyRate
		info.atGateCount = s.AtGateCount
		info.atGateCorrect = s.AtGateCorrect
		info.atGateAccuracyRate = s.AtGateAccuracyRate
		info.postIncidentCount = s.PostIncidentCount
		info.postIncidentCorrect = s.PostIncidentCorrect
		info.postIncidentAccuracyRate = s.PostIncidentAccuracyRate
		info.remediationFeedbackCount = s.RemediationFeedbackCount
		info.remediationCorrectCount = s.RemediationCorrectCount
		info.remediationAccuracyRate = s.RemediationAccuracyRate
		info.remediationAtGateCount = s.RemediationAtGateCount
		info.remediationAtGateCorrect = s.RemediationAtGateCorrect
		info.remediationPostIncidentCount = s.RemediationPostIncidentCount
		info.remediationPostIncidentCorrect = s.RemediationPostIncidentCorrect
		info.avgStepCount = s.AvgStepCount
		info.avgRecoverySecs = s.AvgRecoverySecs
	}
	return info
}

// fetchRootCauseClasses fetches the root_cause_classes for a playbook series
// from the gateway. Returns empty slice when the playbook has no taxonomy or
// on any error. Also returns the taxonomy version string.
func fetchRootCauseClasses(gatewayURL, apiKey, seriesID string) (classes []string, version string) {
	if gatewayURL == "" || seriesID == "" {
		return nil, ""
	}
	reqURL := gatewayURL + "/api/v1/fleet/playbooks?series_id=" + seriesID
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, ""
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, ""
	}
	defer resp.Body.Close()

	var result struct {
		Playbooks []struct {
			RootCauseClasses *struct {
				Version string   `json:"version"`
				Classes []string `json:"classes"`
			} `json:"root_cause_classes"`
		} `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Playbooks) == 0 {
		return nil, ""
	}
	rcc := result.Playbooks[0].RootCauseClasses
	if rcc == nil || len(rcc.Classes) == 0 {
		return nil, ""
	}
	return rcc.Classes, rcc.Version
}

// stabilityInfo is a lightweight view of one fault's stability cert for vault list.
type stabilityInfo struct {
	IsStable              bool
	NRuns                 int
	TestedAt              time.Time
	PrimaryAttribution    string
	AttributionConsistent bool
	hasData               bool
}

// fetchStabilityCert fetches the stability cert for a single fault from the gateway.
// Returns nil when not found or on error.
func fetchStabilityCert(gatewayURL, apiKey, faultID string) *struct {
	FaultID                 string         `json:"fault_id"`
	FaultName               string         `json:"fault_name"`
	PlaybookSeriesID        string         `json:"playbook_series_id"`
	DiagnosisModel          string         `json:"diagnosis_model"`
	JudgeModel              string         `json:"judge_model"`
	NRuns                   int            `json:"n_runs"`
	PassRate                float64        `json:"pass_rate"`
	ConfRangePP             int            `json:"conf_range_pp"`
	IsStable                bool           `json:"is_stable"`
	TestedAt                string         `json:"tested_at"`
	PrimaryAttribution      string         `json:"primary_attribution"`
	AttributionConsistent   bool           `json:"attribution_consistent"`
	AttributionDistribution map[string]int `json:"attribution_distribution"`
	JudgeSpread             float64        `json:"judge_spread"`
	TaxonomyVersion         string         `json:"taxonomy_version"`
} {
	if gatewayURL == "" {
		return nil
	}
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/fault-stability/" + faultID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	var cert struct {
		FaultID                 string         `json:"fault_id"`
		FaultName               string         `json:"fault_name"`
		PlaybookSeriesID        string         `json:"playbook_series_id"`
		DiagnosisModel          string         `json:"diagnosis_model"`
		JudgeModel              string         `json:"judge_model"`
		NRuns                   int            `json:"n_runs"`
		PassRate                float64        `json:"pass_rate"`
		ConfRangePP             int            `json:"conf_range_pp"`
		IsStable                bool           `json:"is_stable"`
		TestedAt                string         `json:"tested_at"`
		PrimaryAttribution      string         `json:"primary_attribution"`
		AttributionConsistent   bool           `json:"attribution_consistent"`
		AttributionDistribution map[string]int `json:"attribution_distribution"`
		JudgeSpread             float64        `json:"judge_spread"`
		TaxonomyVersion         string         `json:"taxonomy_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cert); err != nil {
		return nil
	}
	return &cert
}

// scanFlag returns the value of --flag=val or --flag val from a []string,
// falling back to defaultVal when not found. Used for quick pre-parse before
// full flag.FlagSet parsing so cmdVault can print the gateway banner before
// dispatching to a subcommand.
func scanFlag(args []string, name, defaultVal string) string {
	prefix := "--" + name + "="
	for i, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
		if (a == "--"+name || a == "-"+name) && i+1 < len(args) {
			return args[i+1]
		}
	}
	return defaultVal
}

// fetchGatewayIdentity calls /health and returns (version, hostname).
// Returns empty strings on any error so callers can degrade gracefully.
func fetchGatewayIdentity(gatewayURL, apiKey string) (version, hostname string) {
	if gatewayURL == "" {
		return "", ""
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(gatewayURL, "/")+"/health", nil)
	if err != nil {
		return "", ""
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	var body struct {
		Version  string `json:"version"`
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", ""
	}
	return body.Version, body.Hostname
}

// printGatewayBanner prints a one-line "Connected to:" header to stdout so
// operators always know which gateway instance their vault command is reading.
func printGatewayBanner(gatewayURL, apiKey string) {
	if gatewayURL == "" {
		return
	}
	version, hostname := fetchGatewayIdentity(gatewayURL, apiKey)
	if version == "" && hostname == "" {
		fmt.Printf("Gateway: %s\n\n", gatewayURL)
		return
	}
	parts := []string{gatewayURL}
	if version != "" {
		parts = append(parts, "version: "+version)
	}
	if hostname != "" {
		parts = append(parts, "host: "+hostname)
	}
	fmt.Printf("Gateway: %s\n\n", strings.Join(parts, "  ·  "))
}

// probeGateway does a lightweight health-check against the gateway and returns
// a non-nil error when the gateway is unreachable or returns an unexpected status.
// Used by vault subcommands to emit an early warning rather than silently rendering
// empty columns when a configured gateway cannot be contacted.
func probeGateway(gatewayURL, apiKey string) error {
	if gatewayURL == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(gatewayURL, "/")+"/api/v1/fleet/playbooks?limit=1", nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("gateway returned %d", resp.StatusCode)
	}
	return nil
}

// fetchStabilityCerts fetches all fault stability certs from the gateway and
// returns a map of fault_id → stabilityInfo. Returns empty map on error.
func fetchStabilityCerts(gatewayURL, apiKey string) map[string]stabilityInfo {
	out := make(map[string]stabilityInfo)
	if gatewayURL == "" {
		return out
	}
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/fault-stability"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return out
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return out
	}
	defer resp.Body.Close()

	var result struct {
		Certs []struct {
			FaultID               string `json:"fault_id"`
			NRuns                 int    `json:"n_runs"`
			IsStable              bool   `json:"is_stable"`
			TestedAt              string `json:"tested_at"`
			PrimaryAttribution    string `json:"primary_attribution"`
			AttributionConsistent bool   `json:"attribution_consistent"`
		} `json:"certs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return out
	}
	for _, c := range result.Certs {
		// With composite PK, ListAll returns multiple rows per fault (one per model),
		// ordered by (fault_id, tested_at DESC). Keep only the first (most recent) cert
		// per fault_id for the vault list display.
		if _, exists := out[c.FaultID]; exists {
			continue
		}
		info := stabilityInfo{
			IsStable:              c.IsStable,
			NRuns:                 c.NRuns,
			PrimaryAttribution:    c.PrimaryAttribution,
			AttributionConsistent: c.AttributionConsistent,
			hasData:               true,
		}
		if c.TestedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, c.TestedAt); err == nil {
				info.TestedAt = t
			}
		}
		out[c.FaultID] = info
	}
	return out
}

func vaultList(args []string) {
	fs := flag.NewFlagSet("vault list", flag.ExitOnError)
	var target string
	var short bool
	fs.StringVar(&target, "target", "", "Filter last-run history by target (agent-conn alias or hostname)")
	fs.BoolVar(&short, "short", false, "Compact view: suppress per-version sub-rows")
	cfg := loadConfig(fs, args)
	// Positional arguments are fault IDs (e.g. `vault list db-connection-refused`).
	if positional := fs.Args(); len(positional) > 0 && len(cfg.FailureIDs) == 0 {
		cfg.FailureIDs = positional
	}

	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
		os.Exit(1)
	}

	failures := FilterFailures(cat, cfg)

	runs, _ := loadHistory()

	// Warn early when a gateway URL is configured but unreachable so the operator
	// understands why STABLE and INCIDENTS columns will be empty. The warning goes
	// to stderr so it does not corrupt piped output from the table.
	if cfg.GatewayURL != "" {
		if err := probeGateway(cfg.GatewayURL, cfg.GatewayAPIKey); err != nil {
			fmt.Fprintf(os.Stderr, "[WARN] gateway %s is unreachable — STABLE and INCIDENTS columns will be empty (%v)\n", cfg.GatewayURL, err)
		}
	}

	// Build last-run lookup from faulttest history: fault_id → (timestamp, passed).
	type lastResult struct {
		ts     string
		passed bool
	}
	lastRun := make(map[string]lastResult)
	for _, run := range runs {
		if target != "" && run.Target != target {
			continue
		}
		for _, r := range run.Results {
			ex := lastRun[r.FailureID]
			if ex.ts == "" || run.Timestamp > ex.ts {
				lastRun[r.FailureID] = lastResult{ts: run.Timestamp, passed: r.Passed}
			}
		}
	}

	if target != "" {
		fmt.Printf("Target: %s\n\n", target)
	}

	// Fetch stability certs once for all faults.
	stabilityCerts := fetchStabilityCerts(cfg.GatewayURL, cfg.GatewayAPIKey)

	const (
		colFault     = 32
		colPlatform  = 10
		colDiag      = 31 // pbs_checkpoint_bgwriter_triage = 30 chars
		colRemed     = 32 // pbs_k8s_scale_to_zero_remediate = 31 chars
		colFaultTest = 22 // "2026-04-18  PASS" or "(never)" or "READY"
		colStable    = 14 // "STABLE(5)" or "UNSTABLE(3)" or "—"
		// incidents column is the remainder
	)
	fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n", colFault, "FAULT", colPlatform, "PLATFORM", colDiag, "DIAG PLAYBOOK", colRemed, "REMED PLAYBOOK", colFaultTest, "LAST TEST", colStable, "STABLE", "INCIDENTS")
	fmt.Println(strings.Repeat("-", colFault+1+colPlatform+1+colDiag+1+colRemed+1+colFaultTest+1+colStable+1+50))

	for _, f := range failures {
		playbookID := f.Remediation.PlaybookID
		diagDisplay := f.DiagnosisPlaybookSeriesID
		if diagDisplay == "" {
			diagDisplay = "-"
		}
		remedDisplay := playbookID
		if remedDisplay == "" {
			remedDisplay = "(none)"
		}

		// ── platform column ───────────────────────────────────────────────
		platform := map[string]string{
			"database":   "any",
			"kubernetes": "k8s",
			"host":       "docker/vm",
			"compound":   "multi",
		}[f.Category]
		if platform == "" {
			platform = f.Category
		}

		// ── fault test column ────────────────────────────────────────────
		last := lastRun[f.ID]
		var faultTestCol string
		switch {
		case playbookID == "" && f.DiagnosisPlaybookSeriesID == "":
			faultTestCol = "NO PLAYBOOK"
		case last.ts == "":
			faultTestCol = "(never)"
		default:
			date := last.ts
			if t, err := time.Parse(time.RFC3339, last.ts); err == nil {
				date = t.Format("2006-01-02")
			} else if len(last.ts) >= 10 {
				date = last.ts[:10]
			}
			result := "FAIL"
			if last.passed {
				result = "PASS"
			}
			faultTestCol = date + "  " + result
		}

		// ── incidents column (gateway) ────────────────────────────────────
		// Resolution rate comes from the remediation playbook (did it fix the problem?).
		// Accuracy comes from the diagnosis playbook (was the root cause correct?).
		// These are independent signals on different series.
		incidentCol := "-"
		if playbookID != "" && cfg.GatewayURL != "" {
			info := fetchPlaybookInfo(cfg.GatewayURL, cfg.GatewayAPIKey, playbookID)
			switch info.check {
			case playbookAuthError:
				incidentCol = "AUTH ERROR"
			case playbookNotFound:
				incidentCol = "MISSING"
			case playbookFound:
				sourceTag := ""
				if info.source != "" {
					sourceTag = "  (" + info.source + ")"
				}
				if info.totalRuns == 0 {
					incidentCol = "0 runs" + sourceTag
					if faultTestCol == "(never)" {
						faultTestCol = "READY"
					}
				} else {
					// Accuracy feedback is submitted against the triage/diagnosis series,
					// not the remediation series — fetch it separately when available.
					feedbackCount := info.feedbackCount
					accuracyRate := info.accuracyRate
					if f.DiagnosisPlaybookSeriesID != "" {
						if diagInfo := fetchPlaybookInfo(cfg.GatewayURL, cfg.GatewayAPIKey, f.DiagnosisPlaybookSeriesID); diagInfo.check == playbookFound && diagInfo.feedbackCount > 0 {
							feedbackCount = diagInfo.feedbackCount
							accuracyRate = diagInfo.accuracyRate
						}
					}
					accuracyStr := "–"
					if feedbackCount > 0 {
						accuracyStr = fmt.Sprintf("%.0f%% accurate", accuracyRate*100)
					}
					effStr := ""
					if info.avgStepCount > 0 && info.avgRecoverySecs > 0 {
						effStr = fmt.Sprintf("  avg: %.1f steps, %s recovery", info.avgStepCount, formatDuration(info.avgRecoverySecs))
					} else if info.avgStepCount > 0 {
						effStr = fmt.Sprintf("  avg: %.1f steps", info.avgStepCount)
					} else if info.avgRecoverySecs > 0 {
						effStr = fmt.Sprintf("  avg: %s recovery", formatDuration(info.avgRecoverySecs))
					}
					incidentCol = fmt.Sprintf("%d runs  %.0f%% resolved  %s%s%s",
						info.totalRuns, info.resolutionRate*100, accuracyStr, effStr, sourceTag)
				}
			}
		}

		// ── stable column ─────────────────────────────────────────────────
		stableCol := "—"
		if si, ok := stabilityCerts[f.ID]; ok && si.hasData {
			label := "UNSTABLE"
			if si.IsStable {
				label = "STABLE"
			}
			stableCol = fmt.Sprintf("%s(%d)", label, si.NRuns)
			// Append attribution label when available.
			if si.PrimaryAttribution != "" && si.PrimaryAttribution != "UNKNOWN" {
				if si.AttributionConsistent {
					stableCol += fmt.Sprintf(" attr=%s", si.PrimaryAttribution)
				} else {
					stableCol += fmt.Sprintf(" attr=%s(split)", si.PrimaryAttribution)
				}
			}
			// Append age in days when older than 14 days.
			if !si.TestedAt.IsZero() {
				age := int(time.Since(si.TestedAt).Hours() / 24)
				if age >= 14 {
					stableCol += fmt.Sprintf(" %dd", age)
				}
			}
		}

		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n", colFault, f.ID, colPlatform, platform, colDiag, diagDisplay, colRemed, remedDisplay, colFaultTest, faultTestCol, colStable, stableCol, incidentCol)

		// Per-version learning signal: show the two newest versions for both
		// the diagnosis (triage) and remediation series so improvements in either
		// phase are visible. Suppressed when --short is set.
		if short {
			continue
		}

		diagSeriesID := f.DiagnosisPlaybookSeriesID
		remedSeriesID := playbookID // may be empty

		// When both series are distinct, label rows so the reader knows which
		// playbook type improved. When only one series is present, no label needed.
		showBoth := diagSeriesID != "" && remedSeriesID != "" && diagSeriesID != remedSeriesID
		diagLabel := ""
		remedLabel := ""
		if showBoth {
			diagLabel = "diag  "
			remedLabel = "remed "
		}

		// printVersionSubRows renders at most 2 version sub-rows for a series.
		// A single-version series prints only a pointer so the operator knows
		// vault history exists without duplicating the main row's data.
		printVersionSubRows := func(seriesID, label string) {
			if seriesID == "" || cfg.GatewayURL == "" {
				return
			}
			versions, err := fetchVersionStats(cfg.GatewayURL, cfg.GatewayAPIKey, seriesID)
			if err != nil || len(versions) < 1 {
				return
			}
			// API returns oldest-first; reverse so active (newest) is first.
			for i, j := 0, len(versions)-1; i < j; i, j = i+1, j-1 {
				versions[i], versions[j] = versions[j], versions[i]
			}
			if len(versions) == 1 {
				fmt.Printf("    %s→ vault versions %s\n", label, seriesID)
				return
			}
			show := versions
			if len(show) > 2 {
				show = show[:2]
			}
			for _, vs := range show {
				active := "  "
				if vs.IsActive {
					active = " *"
				}
				effStr := ""
				if vs.AvgStepCount > 0 && vs.AvgRecoverySecs > 0 {
					effStr = fmt.Sprintf("  avg: %.1f steps, %s recovery", vs.AvgStepCount, formatDuration(vs.AvgRecoverySecs))
				} else if vs.AvgStepCount > 0 {
					effStr = fmt.Sprintf("  avg: %.1f steps", vs.AvgStepCount)
				} else if vs.AvgRecoverySecs > 0 {
					effStr = fmt.Sprintf("  avg: %s recovery", formatDuration(vs.AvgRecoverySecs))
				}
				fbStr := ""
				if vs.RemFeedbackCount > 0 {
					fbStr = fmt.Sprintf("  %.0f%% approach OK", vs.RemFeedbackRate*100)
				}
				fmt.Printf("    %s%-5s%s  %dr  %.0f%%%s%s\n",
					label, vs.Version, active, vs.TotalRuns, (vs.ResolutionRate+vs.TransitionRate)*100, effStr, fbStr)
			}
			if len(versions) > 2 {
				fmt.Printf("    %s→ vault versions %s\n", label, seriesID)
			}
		}

		if diagSeriesID != "" {
			printVersionSubRows(diagSeriesID, diagLabel)
		} else {
			// No diagnosis series: fall back to remediation so something is shown.
			printVersionSubRows(remedSeriesID, "")
		}
		if showBoth {
			printVersionSubRows(remedSeriesID, remedLabel)
		}
	}
}

// ── vault status ──────────────────────────────────────────────────────────

func vaultStatus(args []string) {
	fs := flag.NewFlagSet("vault status", flag.ExitOnError)
	var sinceDays int
	var target, fault string
	fs.IntVar(&sinceDays, "since-days", 30, "Days of history to show")
	fs.StringVar(&target, "target", "", "Filter by target (agent-conn alias or hostname)")
	fs.StringVar(&fault, "fault", "", "Filter by fault ID (e.g. db-max-connections)")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	runs, err := loadHistory()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(runs) == 0 {
		fmt.Println("No history found. Run 'faulttest run' first to record results.")
		return
	}

	cutoff := time.Now().AddDate(0, 0, -sinceDays)

	var filtered []historyRun
	for _, run := range runs {
		if target != "" && run.Target != target {
			continue
		}
		t, err := time.Parse(time.RFC3339, run.Timestamp)
		if err != nil || !t.Before(cutoff) {
			filtered = append(filtered, run)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp < filtered[j].Timestamp
	})

	targetLabel := "all targets"
	if target != "" {
		targetLabel = target
	}
	header := fmt.Sprintf("=== Vault Status — %s (last %d days, %d runs)", targetLabel, sinceDays, len(filtered))
	if fault != "" {
		header += fmt.Sprintf(", fault: %s", fault)
	}
	fmt.Println(header + " ===\n")

	if target == "" {
		fmt.Printf("%-10s %-20s %-20s %-6s %s\n", "DATE", "TARGET", "RUN ID", "JUDGE", "PASS RATE")
		fmt.Println(strings.Repeat("-", 78))
	} else {
		fmt.Printf("%-10s %-20s %-6s %s\n", "DATE", "RUN ID", "JUDGE", "PASS RATE")
		fmt.Println(strings.Repeat("-", 58))
	}
	for _, run := range filtered {
		var date string
		if t, err := time.Parse(time.RFC3339, run.Timestamp); err == nil {
			date = t.Format("2006-01-02")
		}
		rate := 0.0
		if run.Total > 0 {
			rate = float64(run.Passed) / float64(run.Total) * 100
		}
		judge := "no"
		if run.JudgeEnabled {
			judge = "yes"
		}
		if target == "" {
			fmt.Printf("%-10s %-20s %-20s %-6s %.0f%% (%d/%d)\n", date, run.Target, run.RunID, judge, rate, run.Passed, run.Total)
		} else {
			fmt.Printf("%-10s %-20s %-6s %.0f%% (%d/%d)\n", date, run.RunID, judge, rate, run.Passed, run.Total)
		}
	}

	// Per-fault detail: group runs by fault ID, show one row per run with all scores.
	type faultRun struct {
		date   string
		runID  string
		result historyFaultResult
	}
	faultRuns := make(map[string][]faultRun)
	faultName := make(map[string]string)
	for _, run := range filtered {
		var date string
		if t, err := time.Parse(time.RFC3339, run.Timestamp); err == nil {
			date = t.Format("2006-01-02")
		}
		for _, r := range run.Results {
			if fault != "" && r.FailureID != fault {
				continue
			}
			faultRuns[r.FailureID] = append(faultRuns[r.FailureID], faultRun{date, run.RunID, r})
			faultName[r.FailureID] = r.FailureName
		}
	}
	if len(faultRuns) == 0 {
		return
	}

	var faultIDs []string
	for id := range faultRuns {
		faultIDs = append(faultIDs, id)
	}
	sort.Strings(faultIDs)

	fmt.Printf("\n=== Per-Fault Detail ===\n")
	//                   date  run   kwd   tools score categ judge remed result
	const rowFmt = "  %-10s %-8s  %5s  %5s  %5s  %5s  %5s  %5s  %s\n"
	for _, id := range faultIDs {
		runs := faultRuns[id]
		fmt.Printf("\n%s (%s)\n", id, faultName[id])
		fmt.Printf(rowFmt, "DATE", "RUN", "KWD", "TOOLS", "SCORE", "CATEG", "JUDGE", "REMED", "RESULT")
		fmt.Println("  " + strings.Repeat("-", 69))
		for _, fr := range runs {
			r := fr.result
			kwd := pct(r.KeywordScore)
			tools := pct(r.ToolScore)
			score := pct(r.Score)
			categ, judge := "-", "-"
			if r.JudgeUsed {
				judge = pct(r.DiagnosisScore)
			} else {
				categ = pct(r.DiagnosisScore)
			}
			remed := "-"
			if r.RemediationScore > 0 || r.OverallScore > 0 {
				remed = pct(r.RemediationScore)
			}
			res := "PASS"
			if !r.Passed {
				res = "FAIL"
			}
			fmt.Printf(rowFmt, fr.date, fr.runID, kwd, tools, score, categ, judge, remed, res)
		}
	}
}

// ── vault drift ───────────────────────────────────────────────────────────

// fetchFaultRunHistory calls the gateway fault-run-history endpoint and returns
// a faultStats map keyed by failure_id, split into first/second halves by mid.
func fetchFaultRunHistory(gatewayURL, apiKey string, sinceDays int, faultID string, cutoff, mid time.Time) (map[string]*struct{ firstHalf, secondHalf []bool }, error) {
	u := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/fault-run-history" +
		"?since_days=" + strconv.Itoa(sinceDays)
	if faultID != "" {
		u += "&fault_id=" + faultID
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fault-run-history: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Entries []struct {
			RunID     string `json:"run_id"`
			FailureID string `json:"failure_id"`
			Timestamp string `json:"timestamp"`
			Passed    bool   `json:"passed"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	type faultHalves struct{ firstHalf, secondHalf []bool }
	out := make(map[string]*faultHalves)
	for _, e := range result.Entries {
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, e.Timestamp)
			if err != nil || ts.Before(cutoff) {
				continue
			}
		}
		if _, ok := out[e.FailureID]; !ok {
			out[e.FailureID] = &faultHalves{}
		}
		if ts.Before(mid) {
			out[e.FailureID].firstHalf = append(out[e.FailureID].firstHalf, e.Passed)
		} else {
			out[e.FailureID].secondHalf = append(out[e.FailureID].secondHalf, e.Passed)
		}
	}
	// Re-key as map[string]*struct{firstHalf, secondHalf []bool}
	result2 := make(map[string]*struct{ firstHalf, secondHalf []bool }, len(out))
	for k, v := range out {
		result2[k] = &struct{ firstHalf, secondHalf []bool }{v.firstHalf, v.secondHalf}
	}
	return result2, nil
}

func vaultDrift(args []string) {
	fs := flag.NewFlagSet("vault drift", flag.ExitOnError)
	var sinceDays int
	var target string
	fs.IntVar(&sinceDays, "since-days", 90, "Days of history to analyze")
	fs.StringVar(&target, "target", "", "Filter by target (agent-conn alias or hostname)")
	cfg := loadConfig(fs, args)

	cutoff := time.Now().AddDate(0, 0, -sinceDays)
	mid := cutoff.Add(time.Duration(sinceDays) * 24 * time.Hour / 2)

	type faultStats struct {
		firstHalf  []bool
		secondHalf []bool
	}
	stats := make(map[string]*faultStats)

	if cfg.GatewayURL != "" {
		// Gateway path: read from auditd fleet-wide history instead of local file.
		gwStats, err := fetchFaultRunHistory(cfg.GatewayURL, cfg.GatewayAPIKey, sinceDays, "", cutoff, mid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching fault run history from gateway: %v\n", err)
			os.Exit(1)
		}
		for id, s := range gwStats {
			stats[id] = &faultStats{firstHalf: s.firstHalf, secondHalf: s.secondHalf}
		}
		if len(stats) == 0 {
			fmt.Println("No history found in gateway (runs need --gateway set when they were posted).")
			return
		}
	} else {
		// Local path: read from ~/.faulttest/history.json.
		runs, err := loadHistory()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(runs) == 0 {
			fmt.Println("No history found.")
			return
		}
		for _, run := range runs {
			if target != "" && run.Target != target {
				continue
			}
			t, err := time.Parse(time.RFC3339, run.Timestamp)
			if err != nil || t.Before(cutoff) {
				continue
			}
			for _, r := range run.Results {
				if _, ok := stats[r.FailureID]; !ok {
					stats[r.FailureID] = &faultStats{}
				}
				if t.Before(mid) {
					stats[r.FailureID].firstHalf = append(stats[r.FailureID].firstHalf, r.Passed)
				} else {
					stats[r.FailureID].secondHalf = append(stats[r.FailureID].secondHalf, r.Passed)
				}
			}
		}
	}

	targetLabel := "all targets"
	if target != "" {
		targetLabel = target
	}
	fmt.Printf("=== Vault Drift Analysis — %s (last %d days) ===\n\n", targetLabel, sinceDays)

	type driftEntry struct {
		id         string
		firstRate  float64
		secondRate float64
		drop       float64
	}
	const minDriftSamples = 3
	var drifted []driftEntry
	suppressed := 0
	for id, s := range stats {
		if len(s.firstHalf) == 0 || len(s.secondHalf) == 0 {
			continue
		}
		if len(s.firstHalf) < minDriftSamples || len(s.secondHalf) < minDriftSamples {
			suppressed++
			continue
		}
		first := passRateOf(s.firstHalf)
		second := passRateOf(s.secondHalf)
		if drop := first - second; drop > 0.20 {
			drifted = append(drifted, driftEntry{id, first, second, drop})
		}
	}

	if len(drifted) == 0 {
		fmt.Println("No significant drift detected (>20% pass rate decline).")
		if suppressed > 0 {
			fmt.Printf("(%d fault(s) suppressed — fewer than %d runs per window half)\n", suppressed, minDriftSamples)
		}
		return
	}

	sort.Slice(drifted, func(i, j int) bool { return drifted[i].drop > drifted[j].drop })

	// Build fault→diagnosis series map from the catalog (best-effort; no gateway needed).
	faultToSeries := make(map[string]string)
	if cat, err := loadActiveCatalog(cfg); err == nil {
		for _, f := range cat.Failures {
			if f.DiagnosisPlaybookSeriesID != "" {
				faultToSeries[f.ID] = f.DiagnosisPlaybookSeriesID
			}
		}
	}

	// Pre-fetch accuracy from gateway when available.
	type accuracyInfo struct {
		rate  float64
		count int
	}
	accuracy := make(map[string]accuracyInfo) // keyed by fault id
	if cfg.GatewayURL != "" {
		for _, d := range drifted {
			if sid, ok := faultToSeries[d.id]; ok {
				info := fetchPlaybookInfo(cfg.GatewayURL, cfg.GatewayAPIKey, sid)
				if info.check == playbookFound && info.feedbackCount > 0 {
					accuracy[d.id] = accuracyInfo{rate: info.accuracyRate, count: info.feedbackCount}
				}
			}
		}
	}

	withAccuracy := cfg.GatewayURL != "" && len(accuracy) > 0
	if withAccuracy {
		fmt.Printf("%-32s %-12s %-12s %-8s %s\n", "FAULT", "FIRST HALF", "SECOND HALF", "DRIFT", "ACCURACY")
		fmt.Println(strings.Repeat("-", 80))
	} else {
		fmt.Printf("%-32s %-12s %-12s %s\n", "FAULT", "FIRST HALF", "SECOND HALF", "DRIFT")
		fmt.Println(strings.Repeat("-", 72))
	}
	for _, d := range drifted {
		driftStr := fmt.Sprintf("-%.0f%%", d.drop*100)
		if withAccuracy {
			accStr := "–"
			if a, ok := accuracy[d.id]; ok {
				accStr = fmt.Sprintf("%.0f%% (%d)", a.rate*100, a.count)
			}
			fmt.Printf("%-32s %-12s %-12s %-8s %s\n", d.id,
				fmt.Sprintf("%.0f%%", d.firstRate*100),
				fmt.Sprintf("%.0f%%", d.secondRate*100),
				driftStr, accStr,
			)
		} else {
			fmt.Printf("%-32s %-12s %-12s %s\n", d.id,
				fmt.Sprintf("%.0f%%", d.firstRate*100),
				fmt.Sprintf("%.0f%%", d.secondRate*100),
				driftStr,
			)
		}
	}
	if suppressed > 0 {
		fmt.Printf("\n(%d fault(s) suppressed — fewer than %d runs per window half)\n", suppressed, minDriftSamples)
	}
}

// pct formats a 0.0-1.0 score as a percentage string.
func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

func passRateOf(results []bool) float64 {
	if len(results) == 0 {
		return 0
	}
	passed := 0
	for _, p := range results {
		if p {
			passed++
		}
	}
	return float64(passed) / float64(len(results))
}

// ── vault accuracy ───────────────────────────────────────────────────────────

// vaultAccuracy shows diagnosis accuracy for a playbook series based on
// operator feedback submitted after incident recovery.
//
// Called with no argument: lists all catalog faults that have a diagnosis
// playbook, fetches feedback stats for each, and shows a discovery table.
//
// Called with a series_id (pbs_*) or fault ID: shows stats for that series.
// Fault IDs are resolved to their DiagnosisPlaybookSeriesID via the catalog.
func vaultAccuracy(args []string) {
	fs := flag.NewFlagSet("vault accuracy", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault accuracy")
		os.Exit(1)
	}

	// No-arg: discovery mode — list all series with feedback.
	if len(fs.Args()) == 0 {
		vaultAccuracyAll(cfg)
		return
	}

	arg := fs.Args()[0]
	seriesID := arg
	faultID := "" // set when arg is a fault ID rather than a series ID

	// If the arg doesn't look like a series_id (pbs_ prefix), treat it as a
	// fault ID and resolve to DiagnosisPlaybookSeriesID via the catalog.
	if !strings.HasPrefix(arg, "pbs_") {
		cat, err := loadActiveCatalog(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
			os.Exit(1)
		}
		var found bool
		for _, f := range cat.Failures {
			if f.ID == arg {
				if f.DiagnosisPlaybookSeriesID == "" {
					fmt.Fprintf(os.Stderr, "Fault %q has no diagnosis playbook — nothing to report.\n", arg)
					os.Exit(1)
				}
				seriesID = f.DiagnosisPlaybookSeriesID
				faultID = f.ID
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "Unknown argument %q: expected a series_id (pbs_...) or a fault ID from the catalog.\n", arg)
			fmt.Fprintln(os.Stderr, "Run `faulttest vault accuracy` with no args to list all series with feedback.")
			os.Exit(1)
		}
	}

	info := fetchPlaybookInfo(cfg.GatewayURL, cfg.GatewayAPIKey, seriesID)
	if info.check != playbookFound {
		fmt.Fprintf(os.Stderr, "Playbook series %q not found in gateway.\n", seriesID)
		// Hint when the user passed a remediation series_id by mistake.
		if strings.Contains(seriesID, "remediat") {
			suggestion := strings.NewReplacer("_remediate", "_triage", "_remediation", "_triage").Replace(seriesID)
			if suggestion != seriesID {
				fmt.Fprintf(os.Stderr, "Note: feedback is recorded on the triage run, not the remediation run.\n")
				fmt.Fprintf(os.Stderr, "Try: faulttest vault accuracy %s\n", suggestion)
			}
		}
		os.Exit(1)
	}

	fmt.Printf("Diagnosis accuracy for series: %s\n\n", seriesID)
	if info.feedbackCount == 0 {
		fmt.Println("  No feedback submitted yet.")
		fmt.Println("  Run a fault test and submit feedback after recovery to populate this report.")
		// Hint when the series looks like a remediation series.
		if strings.Contains(seriesID, "remediat") {
			suggestion := strings.NewReplacer("_remediate", "_triage", "_remediation", "_triage").Replace(seriesID)
			if suggestion != seriesID {
				fmt.Println()
				fmt.Printf("  Note: feedback is recorded on the triage run, not the remediation run.\n")
				fmt.Printf("  Try: faulttest vault accuracy %s\n", suggestion)
			}
		} else {
			fmt.Println()
			fmt.Println("  Tip: run `faulttest vault accuracy` (no args) to list all series with feedback.")
		}
		if faultID != "" {
			printFaultStabilityCert(cfg.GatewayURL, cfg.GatewayAPIKey, faultID, cfg.DiagnosisModel)
		}
		return
	}
	fmt.Printf("  Feedback submitted : %d runs\n", info.feedbackCount)
	fmt.Printf("  Correct diagnoses  : %d\n", info.correctCount)
	fmt.Printf("  Accuracy rate      : %.0f%%\n", info.accuracyRate*100)

	// Breakdown by feedback time when at least one type has data.
	if info.atGateCount > 0 || info.postIncidentCount > 0 {
		fmt.Println()
		fmt.Println("  Breakdown by feedback time:")
		if info.atGateCount > 0 {
			fmt.Printf("    At-gate (before remediation) : %d of %d correct (%.0f%%)\n",
				info.atGateCorrect, info.atGateCount, info.atGateAccuracyRate*100)
		}
		if info.postIncidentCount > 0 {
			fmt.Printf("    Post-incident (after recovery): %d of %d correct (%.0f%%)\n",
				info.postIncidentCorrect, info.postIncidentCount, info.postIncidentAccuracyRate*100)
		}
	}

	// Remediation approach accuracy when feedback exists.
	if info.remediationFeedbackCount > 0 {
		fmt.Println()
		fmt.Printf("Remediation accuracy\n")
		fmt.Printf("  Feedback submitted : %d runs\n", info.remediationFeedbackCount)
		fmt.Printf("  Appropriate        : %d\n", info.remediationCorrectCount)
		fmt.Printf("  Accuracy rate      : %.0f%%\n", info.remediationAccuracyRate*100)
		if info.remediationAtGateCount > 0 || info.remediationPostIncidentCount > 0 {
			fmt.Println()
			fmt.Println("  Breakdown by feedback time:")
			if info.remediationAtGateCount > 0 {
				fmt.Printf("    At-gate (before remediation) : %d of %d appropriate (%.0f%%)\n",
					info.remediationAtGateCorrect, info.remediationAtGateCount,
					float64(info.remediationAtGateCorrect)/float64(info.remediationAtGateCount)*100)
			}
			if info.remediationPostIncidentCount > 0 {
				fmt.Printf("    Post-incident (after recovery): %d of %d appropriate (%.0f%%)\n",
					info.remediationPostIncidentCorrect, info.remediationPostIncidentCount,
					float64(info.remediationPostIncidentCorrect)/float64(info.remediationPostIncidentCount)*100)
			}
		}
	}

	// Triage consistency certification — shown when a fault ID was given
	// (not a bare series ID, where we don't know which fault to look up).
	if faultID != "" {
		printFaultStabilityCert(cfg.GatewayURL, cfg.GatewayAPIKey, faultID, cfg.DiagnosisModel)
	}
}

// printFaultStabilityCert fetches and prints the stability cert for faultID.
// currentModel, when non-empty, is compared against the cert's diagnosis_model
// and a warning is printed when they differ.
func printFaultStabilityCert(gatewayURL, apiKey, faultID, currentModel string) {
	fmt.Println()
	fmt.Println("Triage consistency")
	cert := fetchStabilityCert(gatewayURL, apiKey, faultID)
	if cert == nil {
		fmt.Println("  Not yet certified — run `faulttest run --repeat N` to generate a stability report.")
		return
	}
	verdict := "UNSTABLE"
	if cert.IsStable {
		verdict = "STABLE"
	}
	if cert.FaultName != "" {
		fmt.Printf("  Fault         : %s  (%s)\n", cert.FaultID, cert.FaultName)
	} else {
		fmt.Printf("  Fault         : %s\n", cert.FaultID)
	}
	fmt.Printf("  Verdict       : %s\n", verdict)
	fmt.Printf("  Runs          : %d\n", cert.NRuns)
	fmt.Printf("  Pass rate     : %.0f%%\n", cert.PassRate*100)
	fmt.Printf("  Conf range    : %dpp  (primary hypothesis, passing runs only)\n", cert.ConfRangePP)
	if cert.PlaybookSeriesID != "" {
		fmt.Printf("  Playbook      : %s\n", cert.PlaybookSeriesID)
	}
	if cert.DiagnosisModel != "" {
		fmt.Printf("  Diagnosis model: %s\n", cert.DiagnosisModel)
	}
	if cert.JudgeModel != "" {
		fmt.Printf("  Judge model   : %s\n", cert.JudgeModel)
	}
	if cert.TestedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, cert.TestedAt); err == nil {
			age := int(time.Since(t).Hours() / 24)
			fmt.Printf("  Tested at     : %s  (%d days ago)\n", t.Format("2006-01-02 15:04 MST"), age)
			if age >= 30 {
				fmt.Println("  ⚠ cert is older than 30 days — consider re-running --repeat to refresh")
			}
		}
	}
	if currentModel != "" && cert.DiagnosisModel != "" && cert.DiagnosisModel != currentModel {
		fmt.Printf("  ⚠ cert was issued for %s but current agent model is %s\n", cert.DiagnosisModel, currentModel)
		fmt.Printf("    Run: faulttest run --repeat N --agent-model %s ... to re-certify\n", currentModel)
	}
	if cert.PrimaryAttribution != "" && cert.PrimaryAttribution != attributionUnknown {
		fmt.Println()
		taxStr := ""
		if cert.TaxonomyVersion != "" {
			taxStr = fmt.Sprintf(" (taxonomy %s)", cert.TaxonomyVersion)
		}
		fmt.Printf("  Attribution%s\n", taxStr)
		totalRuns := 0
		for _, v := range cert.AttributionDistribution {
			totalRuns += v
		}
		primaryCount := cert.AttributionDistribution[cert.PrimaryAttribution]
		consistent := "yes"
		if !cert.AttributionConsistent {
			consistent = "no (split)"
		}
		fmt.Printf("  Primary class  : %s\n", cert.PrimaryAttribution)
		if totalRuns > 0 {
			fmt.Printf("  Consistent     : %s  (%d/%d runs)\n", consistent, primaryCount, totalRuns)
		} else {
			fmt.Printf("  Consistent     : %s\n", consistent)
		}
		if len(cert.AttributionDistribution) > 1 {
			var parts []string
			for k, v := range cert.AttributionDistribution {
				parts = append(parts, fmt.Sprintf("%s=%d", k, v))
			}
			sort.Strings(parts)
			fmt.Printf("  Distribution   : %s\n", strings.Join(parts, ", "))
		}
		if cert.JudgeSpread > 1e-10 {
			fmt.Printf("  Judge spread   : %.2f\n", cert.JudgeSpread)
		}
	}
}

// vaultAccuracyAll is the no-arg mode: scans every catalog fault that has a
// DiagnosisPlaybookSeriesID, fetches feedback stats for each, and prints a
// summary table grouped by whether feedback has been submitted.
func vaultAccuracyAll(cfg *HarnessConfig) {
	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
		os.Exit(1)
	}

	type entry struct {
		faultID                        string
		seriesID                       string
		atGateCount                    int
		atGateCorrect                  int
		postIncidentCount              int
		postIncidentCorrect            int
		rate                           float64
		remediationFeedbackCount       int
		remediationCorrectCount        int
		remediationAccuracyRate        float64
	}

	seen := make(map[string]bool)
	var withFeedback, withoutFeedback []entry
	for _, f := range cat.Failures {
		sid := f.DiagnosisPlaybookSeriesID
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true
		info := fetchPlaybookInfo(cfg.GatewayURL, cfg.GatewayAPIKey, sid)
		e := entry{faultID: f.ID, seriesID: sid}
		if info.check == playbookFound {
			e.atGateCount = info.atGateCount
			e.atGateCorrect = info.atGateCorrect
			e.postIncidentCount = info.postIncidentCount
			e.postIncidentCorrect = info.postIncidentCorrect
			e.rate = info.accuracyRate
			e.remediationFeedbackCount = info.remediationFeedbackCount
			e.remediationCorrectCount = info.remediationCorrectCount
			e.remediationAccuracyRate = info.remediationAccuracyRate
		}
		if e.atGateCount+e.postIncidentCount > 0 {
			withFeedback = append(withFeedback, e)
		} else {
			withoutFeedback = append(withoutFeedback, e)
		}
	}

	if len(withFeedback) == 0 && len(withoutFeedback) == 0 {
		fmt.Println("No faults with diagnosis playbooks found in catalog.")
		return
	}

	fmtBreakdown := func(correct, total int) string {
		if total == 0 {
			return "–"
		}
		return fmt.Sprintf("%d/%d", correct, total)
	}

	fmtAccuracy := func(rate float64, count int) string {
		if count == 0 {
			return "–"
		}
		return fmt.Sprintf("%.0f%%", rate*100)
	}

	if len(withFeedback) > 0 {
		colFault := 36
		colSeries := 36
		fmt.Printf("  %-*s %-*s %9s %9s %9s %9s\n", colFault, "FAULT", colSeries, "SERIES", "AT-GATE", "POST-INC", "DIAG ACC", "REMED ACC")
		fmt.Printf("  %-*s %-*s %9s %9s %9s %9s\n", colFault, strings.Repeat("─", colFault), colSeries, strings.Repeat("─", colSeries), "─────────", "────────", "────────", "─────────")
		for _, e := range withFeedback {
			fmt.Printf("  %-*s %-*s %9s %9s %9s %9s\n",
				colFault, e.faultID, colSeries, e.seriesID,
				fmtBreakdown(e.atGateCorrect, e.atGateCount),
				fmtBreakdown(e.postIncidentCorrect, e.postIncidentCount),
				fmtAccuracy(e.rate, e.atGateCount+e.postIncidentCount),
				fmtAccuracy(e.remediationAccuracyRate, e.remediationFeedbackCount))
		}
		fmt.Println()
		fmt.Println("  Run `faulttest vault accuracy <series_id or fault_id>` for the full breakdown.")
	} else {
		fmt.Println("  No feedback has been submitted yet.")
	}

	if len(withoutFeedback) > 0 {
		noFeedbackSeries := make([]string, len(withoutFeedback))
		for i, e := range withoutFeedback {
			noFeedbackSeries[i] = e.seriesID
		}
		fmt.Printf("\n  %d series awaiting first feedback: %s\n", len(withoutFeedback), strings.Join(noFeedbackSeries, ", "))
	}
}

// ── vault incidents ───────────────────────────────────────────────────────

// incidentRun is a minimal view of a playbook run used for the incidents table.
type incidentRun struct {
	RunID           string `json:"run_id"`
	SeriesID        string `json:"series_id"`
	Outcome         string `json:"outcome"`
	Operator        string `json:"operator"`
	StartedAt       string `json:"started_at"`
	CompletedAt     string `json:"completed_at"`
	FindingsSummary string `json:"findings_summary"`
	PriorRunID      string `json:"prior_run_id"`
	TraceID         string `json:"trace_id"`
}

// incidentFeedback is the feedback response shape from GET .../feedback.
type incidentFeedback struct {
	RunID          string `json:"run_id"`
	VerdictCorrect *bool  `json:"verdict_correct"`
	VerdictNotes   string `json:"verdict_notes"`
	Operator       string `json:"operator"`
}

// fetchRunsByOutcome calls GET /api/v1/fleet/playbook-runs?outcome=<o>&limit=<n>.
func fetchRunsByOutcome(gatewayURL, apiKey, outcome string, limit int) ([]incidentRun, error) {
	params := neturl.Values{"outcome": {outcome}, "limit": {fmt.Sprintf("%d", limit)}}
	u := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbook-runs?" + params.Encode()
	return doFetchRuns(u, apiKey)
}

// fetchRunsBySeries calls GET /api/v1/fleet/playbook-runs?series_id=<sid>&limit=<n>.
func fetchRunsBySeries(gatewayURL, apiKey, seriesID string, limit int) ([]incidentRun, error) {
	url := strings.TrimSuffix(gatewayURL, "/") +
		fmt.Sprintf("/api/v1/fleet/playbook-runs?series_id=%s&limit=%d", seriesID, limit)
	return doFetchRuns(url, apiKey)
}

// doFetchRuns executes GET on a pre-built playbook-runs URL and decodes the result.
func doFetchRuns(url, apiKey string) ([]incidentRun, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Runs []incidentRun `json:"runs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return result.Runs, nil
}

// pickBestRunForSuggest returns the run ID of the best run for synthesis:
// prefers "resolved" (which has full tool execution data) over "transitioned"
// (which may have completed the handoff without tool calls to synthesize from).
func pickBestRunForSuggest(gatewayURL, apiKey, seriesID string) (string, error) {
	runs, err := fetchRunsBySeries(gatewayURL, apiKey, seriesID, 20)
	if err != nil {
		return "", err
	}
	// First pass: prefer resolved runs — they have complete step data.
	var bestRunID string
	for _, r := range runs {
		if r.Outcome == "resolved" {
			bestRunID = r.RunID
			break
		}
	}
	// Second pass: fall back to transitioned if no resolved run exists.
	if bestRunID == "" {
		for _, r := range runs {
			if r.Outcome == "transitioned" {
				bestRunID = r.RunID
				break
			}
		}
	}
	if bestRunID == "" {
		return "", fmt.Errorf("no resolved or transitioned runs found for series %s (check: faulttest vault incidents %s)", seriesID, seriesID)
	}
	// Fleet-mode playbook runs log tool execution events under the faulttest
	// audit trace (e.g. "faulttest-{hash}-{fault-id}"), not the plr_* run ID.
	// Resolve via the incident's triage journey so from-trace receives the
	// trace that actually has audit_events entries.
	if n, ferr := fetchIncidentNarrative(gatewayURL, apiKey, bestRunID); ferr == nil {
		for _, j := range n.Journeys {
			if j.Phase == "triage" && j.TraceID != "" {
				return j.TraceID, nil
			}
		}
	}
	return bestRunID, nil
}

// fetchFeedback calls GET /api/v1/fleet/playbook-runs/{runID}/feedback.
// Returns the triage/at_gate record when present, falling back to triage/post_incident.
// Returns nil when no triage feedback has been submitted.
func fetchFeedback(gatewayURL, apiKey, runID string) *incidentFeedback {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbook-runs/" + runID + "/feedback"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	defer resp.Body.Close()
	var envelope struct {
		Feedback []struct {
			RunID          string `json:"run_id"`
			FeedbackType   string `json:"feedback_type"`
			FeedbackTime   string `json:"feedback_time"`
			VerdictCorrect *bool  `json:"verdict_correct"`
			VerdictNotes   string `json:"verdict_notes"`
			Operator       string `json:"operator"`
		} `json:"feedback"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil
	}
	// Prefer triage/at_gate, fall back to triage/post_incident.
	var fallback *incidentFeedback
	for _, fb := range envelope.Feedback {
		if fb.FeedbackType != "triage" {
			continue
		}
		f := &incidentFeedback{
			RunID:          fb.RunID,
			VerdictCorrect: fb.VerdictCorrect,
			VerdictNotes:   fb.VerdictNotes,
			Operator:       fb.Operator,
		}
		if fb.FeedbackTime == "at_gate" {
			return f
		}
		if fallback == nil {
			fallback = f
		}
	}
	return fallback
}

// fetchRemediationRun fetches the remediation run linked to a triage run via
// GET /api/v1/fleet/playbook-runs?prior_run_id={triageRunID}&limit=1.
// Returns nil when no remediation run exists for the triage run.
func fetchRemediationRun(gatewayURL, apiKey, triageRunID string) *incidentRun {
	url := strings.TrimSuffix(gatewayURL, "/") +
		"/api/v1/fleet/playbook-runs?prior_run_id=" + triageRunID + "&limit=1"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		Runs []incidentRun `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Runs) == 0 {
		return nil
	}
	return &result.Runs[0]
}

// faultFromTraceID extracts the fault ID from a faulttest trace ID of the form
// "faulttest-{8hex}-{fault-id}-r{N}". Returns "" for non-faulttest traces.
func faultFromTraceID(traceID string) string {
	const prefix = "faulttest-"
	if !strings.HasPrefix(traceID, prefix) {
		return ""
	}
	rest := traceID[len(prefix):]
	// Strip leading 8-char hex segment + dash.
	if len(rest) > 9 && rest[8] == '-' {
		rest = rest[9:]
	}
	// Strip trailing run-counter suffix (-r{N}).
	if i := strings.LastIndex(rest, "-r"); i > 0 {
		rest = rest[:i]
	}
	return rest
}

// formatRemediationOutcome formats a remediation run as "resolved 8.1s", "abandoned", etc.
func formatRemediationOutcome(r *incidentRun) string {
	if r == nil {
		return "–"
	}
	outcome := r.Outcome
	if outcome == "" {
		outcome = "unknown"
	}
	if r.StartedAt != "" && r.CompletedAt != "" {
		t0, err0 := time.Parse(time.RFC3339, r.StartedAt)
		t1, err1 := time.Parse(time.RFC3339, r.CompletedAt)
		if err0 == nil && err1 == nil && t1.After(t0) {
			secs := t1.Sub(t0).Seconds()
			if secs < 60 {
				return fmt.Sprintf("%s %.1fs", outcome, secs)
			}
			return fmt.Sprintf("%s %.0fm", outcome, secs/60)
		}
	}
	return outcome
}

// vaultIncidents lists incidents (triage run IDs) for a fault or playbook series,
// including outcome, timestamp, truncated findings, and feedback status.
// vaultIncidentsRecent shows the most recent playbook runs across all faults
// by querying resolved and failed outcomes and merging the results.
func vaultIncidentsRecent(cfg *HarnessConfig, limit int, details bool) {
	outcomes := []string{"resolved", "transitioned", "failed", "abandoned", "escalated", "escalated+resolved"}
	seen := map[string]bool{}
	var all []incidentRun
	for _, o := range outcomes {
		runs, err := fetchRunsByOutcome(cfg.GatewayURL, cfg.GatewayAPIKey, o, limit)
		if err != nil {
			continue
		}
		for _, r := range runs {
			if !seen[r.RunID] {
				seen[r.RunID] = true
				all = append(all, r)
			}
		}
	}

	if len(all) == 0 {
		fmt.Println("No recent incidents found.")
		fmt.Println("Run `faulttest vault incidents <fault-id>` to filter by fault.")
		return
	}

	// Sort by started_at descending.
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartedAt > all[j].StartedAt
	})
	if len(all) > limit {
		all = all[:limit]
	}

	// Optionally fetch per-run narratives for journey count and source detection.
	type extra struct {
		journeyCount int
		source       string // "injected" or "real" or ""
	}
	extras := make([]extra, len(all))
	if details {
		fmt.Fprintf(os.Stderr, "Fetching details for %d runs...\n", len(all))
		for i, run := range all {
			n, err := fetchIncidentNarrative(cfg.GatewayURL, cfg.GatewayAPIKey, run.RunID)
			if err != nil {
				continue
			}
			extras[i].journeyCount = len(n.Journeys)
			// Detect injected: any journey trace_id starting with "faulttest-".
			for _, j := range n.Journeys {
				if strings.HasPrefix(j.TraceID, "faulttest-") {
					extras[i].source = "injected"
					break
				}
			}
			if extras[i].source == "" && len(n.Journeys) > 0 {
				extras[i].source = "real"
			}
		}
	}

	fmt.Printf("Recent incidents (last %d)", len(all))
	if details {
		fmt.Printf(" — SOURCE: injected=faulttest harness, real=human operator")
	}
	fmt.Printf("\n\n")

	const (
		colRunID   = 14
		colSeries  = 28
		colDate    = 16
		colOutcome = 18 // wide enough for "escalated+resolved"
		colOp      = 20
		colJourney = 8
		colSource  = 8
	)
	if details {
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			colRunID, "RUN ID", colSeries, "SERIES", colDate, "STARTED",
			colOutcome, "OUTCOME", colJourney, "JOURNEYS", colSource, "SOURCE", "OPERATOR")
		fmt.Println(strings.Repeat("─", colRunID+2+colSeries+2+colDate+2+colOutcome+2+colJourney+2+colSource+2+colOp))
	} else {
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n",
			colRunID, "RUN ID", colSeries, "SERIES", colDate, "STARTED", colOutcome, "OUTCOME", "OPERATOR")
		fmt.Println(strings.Repeat("─", colRunID+2+colSeries+2+colDate+2+colOutcome+2+colOp+4))
	}

	for i, run := range all {
		date := run.StartedAt
		if t, err := time.Parse(time.RFC3339, run.StartedAt); err == nil {
			date = t.Format("2006-01-02 15:04")
		} else if len(run.StartedAt) >= 16 {
			date = run.StartedAt[:16]
		}
		series := run.SeriesID
		if len(series) > colSeries {
			series = series[:colSeries-3] + "..."
		}
		op := run.Operator
		if op == "" {
			op = "–"
		}
		if details {
			jc := "–"
			if extras[i].journeyCount > 0 {
				jc = fmt.Sprintf("%d", extras[i].journeyCount)
			}
			src := extras[i].source
			if src == "" {
				src = "–"
			}
			fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
				colRunID, run.RunID, colSeries, series, colDate, date,
				colOutcome, run.Outcome, colJourney, jc, colSource, src, op)
		} else {
			fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n",
				colRunID, run.RunID, colSeries, series, colDate, date, colOutcome, run.Outcome, op)
		}
	}
	fmt.Println()
	fmt.Println("  → vault incidents <plr_*>           full incident narrative")
	fmt.Println("  → vault incidents <fault-id>        all runs for a fault")
	fmt.Println("  → vault incidents --details         show JOURNEYS count and SOURCE")
}

// Usage: faulttest vault incidents <fault-id or series-id> [--limit N]
func vaultIncidents(args []string) {
	fs := flag.NewFlagSet("vault incidents", flag.ExitOnError)
	var limit int
	var details bool
	fs.IntVar(&limit, "limit", 20, "Maximum number of incidents to show")
	fs.BoolVar(&details, "details", false, "Fetch per-run journey count and source (slower; makes one extra API call per run)")
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault incidents")
		os.Exit(1)
	}
	if len(fs.Args()) == 0 {
		vaultIncidentsRecent(cfg, limit, details)
		return
	}

	arg := fs.Args()[0]

	// Deep-dive mode: when arg is a run ID (plr_*), print the full incident journey.
	if strings.HasPrefix(arg, "plr_") {
		if cfg.GatewayURL == "" {
			fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault incidents <run-id>")
			os.Exit(1)
		}
		printIncidentJourney(cfg.GatewayURL, cfg.GatewayAPIKey, arg)
		return
	}

	seriesID := arg
	faultID := ""

	// Resolve fault ID ↔ diagnosis series ID via catalog.
	if !strings.HasPrefix(arg, "pbs_") {
		// arg is a fault ID — resolve to series and keep fault for display.
		faultID = arg
		cat, err := loadActiveCatalog(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
			os.Exit(1)
		}
		var found bool
		for _, f := range cat.Failures {
			if f.ID == arg {
				if f.DiagnosisPlaybookSeriesID == "" {
					fmt.Fprintf(os.Stderr, "Fault %q has no diagnosis playbook.\n", arg)
					os.Exit(1)
				}
				seriesID = f.DiagnosisPlaybookSeriesID
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "Unknown fault ID %q. Run `faulttest list` to see available faults.\n", arg)
			os.Exit(1)
		}
	} else {
		// arg is a series ID — reverse-lookup to find the fault that references it.
		if cat, err := loadActiveCatalog(cfg); err == nil {
			for _, f := range cat.Failures {
				if f.DiagnosisPlaybookSeriesID == arg {
					faultID = f.ID
					break
				}
			}
		}
	}

	runs, err := fetchRunsBySeries(cfg.GatewayURL, cfg.GatewayAPIKey, seriesID, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching incidents: %v\n", err)
		os.Exit(1)
	}
	if len(runs) == 0 {
		fmt.Printf("No incidents found for series %q.\n", seriesID)
		return
	}

	fmt.Printf("Incidents for %s (%s) — %d runs\n\n", arg, seriesID, len(runs))

	const (
		colRunID    = 14
		colDate     = 16
		colFault    = 28
		colDiag     = 18 // wide enough for "escalated+resolved"
		colRemed    = 24 // wide enough for "escalated+resolved 30.0s"
		colFeedback = 12
		colScore    = 5
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colRunID, "RUN ID", colDate, "STARTED", colFault, "FAULT",
		colDiag, "DIAG", colRemed, "REMEDIATION",
		colFeedback, "FEEDBACK", colScore, "SCORE", "FINDINGS")
	fmt.Println(strings.Repeat("─", colRunID+2+colDate+2+colFault+2+colDiag+2+colRemed+2+colFeedback+2+colScore+2+40))

	for _, run := range runs {
		date := run.StartedAt
		if t, err := time.Parse(time.RFC3339, run.StartedAt); err == nil {
			date = t.Format("2006-01-02 15:04")
		} else if len(run.StartedAt) >= 16 {
			date = run.StartedAt[:16]
		}

		diagOutcome := run.Outcome
		if diagOutcome == "" {
			diagOutcome = "unknown"
		}

		remed := fetchRemediationRun(cfg.GatewayURL, cfg.GatewayAPIKey, run.RunID)
		remedStr := formatRemediationOutcome(remed)

		fb := fetchFeedback(cfg.GatewayURL, cfg.GatewayAPIKey, run.RunID)
		feedbackStr := "–"
		if fb != nil {
			if fb.VerdictCorrect == nil {
				feedbackStr = "submitted"
			} else if *fb.VerdictCorrect {
				feedbackStr = "✓ correct"
			} else {
				feedbackStr = "✗ wrong"
			}
		}

		ev := fetchEvaluation(cfg.GatewayURL, cfg.GatewayAPIKey, run.RunID)
		scoreStr := "–"
		if ev != nil {
			scoreStr = fmt.Sprintf("%d%%", int(ev.OverallScore*100))
		}

		// Prefer the fault ID embedded in the trace ID (faulttest-{uuid}-{fault}-r{N})
		// over the static reverse-lookup, which only returns one fault per series.
		faultDisplay := faultID
		if id := faultFromTraceID(run.TraceID); id != "" {
			faultDisplay = id
		}
		if faultDisplay == "" {
			faultDisplay = "–"
		}
		if len(faultDisplay) > colFault {
			faultDisplay = faultDisplay[:colFault-3] + "..."
		}

		findings := run.FindingsSummary
		if len(findings) > 80 {
			findings = findings[:77] + "..."
		}
		if findings == "" {
			findings = "–"
		}

		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			colRunID, run.RunID,
			colDate, date,
			colFault, faultDisplay,
			colDiag, diagOutcome,
			colRemed, remedStr,
			colFeedback, feedbackStr,
			colScore, scoreStr,
			findings,
		)
	}

	fmt.Println()
	fmt.Printf("To submit feedback:\n")
	fmt.Printf("  curl -sX POST %s/api/v1/decisions/feedback:<run_id>/resolve \\\n", cfg.GatewayURL)
	fmt.Printf("    -H 'Authorization: Bearer $API_KEY' -H 'Content-Type: application/json' \\\n")
	fmt.Printf("    -d '{\"resolution\": \"approved\", \"resolved_by\": \"you@example.com\", \"reason\": \"<root cause>\"}'\n")
	fmt.Printf("  (resolution=approved → correct, resolution=denied → wrong diagnosis)\n")
}

// ── vault journey ─────────────────────────────────────────────────────────

// journeySummary mirrors audit.JourneySummary for JSON decoding.
type journeySummary struct {
	TraceID       string               `json:"trace_id"`
	StartedAt     string               `json:"started_at"`
	EndedAt       string               `json:"ended_at"`
	DurationMs    int64                `json:"duration_ms"`
	UserID        string               `json:"user_id,omitempty"`
	UserQuery     string               `json:"user_query,omitempty"`
	Agent         string               `json:"agent,omitempty"`
	Category      string               `json:"category,omitempty"`
	Delegations   []delegationSummary  `json:"delegations,omitempty"`
	ToolsUsed     []string             `json:"tools_used"`
	Outcome       string               `json:"outcome,omitempty"`
	EventCount    int                  `json:"event_count"`
	RetryCount    int                  `json:"retry_count,omitempty"`
	Origin        string               `json:"origin,omitempty"`
	HasMismatch   bool                 `json:"has_mismatch,omitempty"`
	IncidentRunID string               `json:"incident_run_id,omitempty"`
}

// delegationSummary mirrors audit.DelegationSummary.
type delegationSummary struct {
	Intent string   `json:"intent"`
	Tools  []string `json:"tools"`
}

// fetchJourneys calls GET /api/v1/governance/journeys with the given query params.
func fetchJourneys(gatewayURL, apiKey string, params map[string]string) ([]journeySummary, error) {
	u := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/governance/journeys"
	if len(params) > 0 {
		parts := make([]string, 0, len(params))
		for k, v := range params {
			parts = append(parts, k+"="+v)
		}
		u += "?" + strings.Join(parts, "&")
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var summaries []journeySummary
	if err := json.Unmarshal(body, &summaries); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return summaries, nil
}

// vaultJourney shows the audit trail for one or more journey traces.
// Usage: faulttest vault journey [<trace_id>] [--limit N] [--since duration] [--category db|k8s] [--outcome X] [--incident]
func vaultJourney(args []string) {
	fs := flag.NewFlagSet("vault journey", flag.ExitOnError)
	var limit int
	var since string
	var category string
	var outcome string
	var incidentOnly bool
	var detail bool
	fs.IntVar(&limit, "limit", 20, "Maximum number of journeys to show")
	fs.StringVar(&since, "since", "24h", "Show journeys from the last duration (e.g. 1h, 24h, 7d)")
	fs.StringVar(&category, "category", "", "Filter by category: database, kubernetes, host")
	fs.StringVar(&outcome, "outcome", "", "Filter by outcome: resolved, abandoned, escalated, in_progress")
	fs.BoolVar(&incidentOnly, "incident", false, "Show only journeys linked to an incident run")
	fs.BoolVar(&detail, "detail", false, "Show agent reasoning interleaved with tool calls (requires incident run ID)")
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault journey")
		os.Exit(1)
	}

	// Detail mode: vault journey <trace_id> [--detail]
	if len(fs.Args()) > 0 {
		traceID := fs.Args()[0]
		printJourneyDetail(cfg.GatewayURL, cfg.GatewayAPIKey, traceID, detail)
		return
	}

	// --incident without an explicit --since: widen to 7d so historical
	// incident runs (which accumulate over days) aren't silently hidden by
	// the 24h default.
	var sinceExplicit bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "since" {
			sinceExplicit = true
		}
	})
	if incidentOnly && !sinceExplicit {
		since = "7d"
	}

	// List mode: show recent journeys.
	params := map[string]string{
		"limit": strconv.Itoa(limit),
	}
	// Normalise "7d" → "168h" since the server expects Go duration format.
	sinceDur := since
	if strings.HasSuffix(since, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(since, "d")); err == nil {
			sinceDur = fmt.Sprintf("%dh", days*24)
		}
	}
	if sinceDur != "" {
		params["since"] = sinceDur
	}
	if category != "" {
		params["category"] = category
	}
	if outcome != "" {
		params["outcome"] = outcome
	}

	if incidentOnly {
		params["incident_only"] = "true"
	}

	journeys, err := fetchJourneys(cfg.GatewayURL, cfg.GatewayAPIKey, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching journeys: %v\n", err)
		os.Exit(1)
	}
	if len(journeys) == 0 {
		fmt.Println("No journeys found.")
		return
	}

	title := fmt.Sprintf("Recent journeys — %d entries (last %s)", len(journeys), since)
	if category != "" {
		title += "  category=" + category
	}
	if outcome != "" {
		title += "  outcome=" + outcome
	}
	fmt.Println(title)
	fmt.Println()

	const (
		colTrace    = 44
		colDate     = 16
		colDur      = 7
		colAgent    = 25
		colOrigin   = 12
		colOutcome  = 17
		colIncident = 14
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colTrace, "TRACE ID",
		colDate, "STARTED",
		colDur, "DUR",
		colAgent, "AGENT",
		colOrigin, "ORIGIN",
		colOutcome, "OUTCOME",
		colIncident, "INCIDENT",
		"TOOLS",
	)
	fmt.Println(strings.Repeat("─", colTrace+2+colDate+2+colDur+2+colAgent+2+colOrigin+2+colOutcome+2+colIncident+2+40))

	for _, j := range journeys {
		date := j.StartedAt
		if t, err := time.Parse(time.RFC3339, j.StartedAt); err == nil {
			date = t.Format("2006-01-02 15:04")
		} else if len(j.StartedAt) >= 16 {
			date = j.StartedAt[:16]
		}

		durStr := "–"
		if j.DurationMs > 0 {
			d := time.Duration(j.DurationMs) * time.Millisecond
			if d < time.Minute {
				durStr = fmt.Sprintf("%.1fs", d.Seconds())
			} else {
				durStr = fmt.Sprintf("%.0fm", d.Minutes())
			}
		}

		agent := j.Agent
		if agent == "" {
			agent = j.Category
		}
		if agent == "" {
			agent = "–"
		}

		originStr := j.Origin
		if originStr == "" {
			originStr = "–"
		}

		outcomeStr := j.Outcome
		if outcomeStr == "" {
			outcomeStr = "–"
		}

		incidentStr := "–"
		if j.IncidentRunID != "" {
			incidentStr = j.IncidentRunID
		}
		mismatchFlag := ""
		if j.HasMismatch {
			mismatchFlag = " !"
		}

		toolStr := strings.Join(j.ToolsUsed, ", ")
		if len(toolStr) > 40 {
			toolStr = toolStr[:37] + "..."
		}
		if toolStr == "" {
			toolStr = "–"
		}

		traceDisplay := j.TraceID
		if len(traceDisplay) > colTrace {
			traceDisplay = traceDisplay[:colTrace]
		}

		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s%s\n",
			colTrace, traceDisplay,
			colDate, date,
			colDur, durStr,
			colAgent, agent,
			colOrigin, originStr,
			colOutcome, outcomeStr,
			colIncident, incidentStr,
			toolStr, mismatchFlag,
		)
	}

	fmt.Println()
	fmt.Println("  ! = fabrication mismatch (agent reported success but no tool call recorded)")
	fmt.Printf("\nTo drill into a trace:\n  faulttest vault journey <trace_id> --gateway %s\n", cfg.GatewayURL)
}

// printJourneyDetail shows a single journey in full detail.
// When detail is true it fetches agent_reasoning + tool_execution events and
// renders them interleaved so the reader can see WHY each tool was called.
func printJourneyDetail(gatewayURL, apiKey, traceID string, detail bool) {
	journeys, err := fetchJourneys(gatewayURL, apiKey, map[string]string{
		"trace_id": traceID,
		"limit":    "1",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching journey: %v\n", err)
		os.Exit(1)
	}
	if len(journeys) == 0 {
		fmt.Fprintf(os.Stderr, "No journey found for trace_id %q\n", traceID)
		os.Exit(1)
	}
	j := journeys[0]

	sep := strings.Repeat("─", 70)
	sectionJ := func(name string) {
		fmt.Printf("\n%s\n%s\n", name, sep)
	}

	fmt.Printf("\nJOURNEY  %s\n%s\n", j.TraceID, sep)

	started := j.StartedAt
	if t, err := time.Parse(time.RFC3339, j.StartedAt); err == nil {
		started = t.Format("2006-01-02 15:04:05 UTC")
	}
	ended := j.EndedAt
	if t, err := time.Parse(time.RFC3339, j.EndedAt); err == nil {
		ended = t.Format("2006-01-02 15:04:05 UTC")
	}
	durStr := "–"
	if j.DurationMs > 0 {
		d := time.Duration(j.DurationMs) * time.Millisecond
		durStr = fmt.Sprintf("%.1fs", d.Seconds())
	}

	fmt.Printf("  %-18s %s\n", "Started:", started)
	fmt.Printf("  %-18s %s\n", "Ended:", ended)
	fmt.Printf("  %-18s %s\n", "Duration:", durStr)
	if j.Agent != "" {
		fmt.Printf("  %-18s %s\n", "Agent:", j.Agent)
	}
	if j.Category != "" {
		fmt.Printf("  %-18s %s\n", "Category:", j.Category)
	}
	if j.Origin != "" {
		fmt.Printf("  %-18s %s\n", "Origin:", j.Origin)
	}
	fmt.Printf("  %-18s %s\n", "Outcome:", j.Outcome)
	fmt.Printf("  %-18s %d\n", "Events:", j.EventCount)
	if j.RetryCount > 0 {
		fmt.Printf("  %-18s %d\n", "Retries:", j.RetryCount)
	}

	if j.UserQuery != "" {
		sectionJ("QUERY")
		q := j.UserQuery
		if idx := strings.IndexByte(q, '\n'); idx >= 0 {
			q = q[:idx] + " ..."
		}
		fmt.Printf("  %s\n", wordWrap(q, 66, "  "))
	}

	if len(j.Delegations) > 0 {
		sectionJ("DELEGATIONS")
		for i, d := range j.Delegations {
			fmt.Printf("  %d. %s\n", i+1, d.Intent)
			if len(d.Tools) > 0 {
				fmt.Printf("     tools: %s\n", strings.Join(d.Tools, ", "))
			}
		}
	}

	if detail && j.IncidentRunID != "" {
		events, err := fetchRunEvents(gatewayURL, apiKey, j.IncidentRunID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not fetch run events: %v\n", err)
		} else {
			sectionJ("EXECUTION TRACE")
			printReasoningTrace(events)
		}
		// Show the structured FINDINGS from the playbook run's diagnostic_report.
		// This is the agent's final conclusion that often falls after the last tool
		// call and is not captured as an agent_reasoning event.
		if runSummary := fetchRunFindings(gatewayURL, apiKey, j.IncidentRunID); runSummary != "" {
			sectionJ("FINDINGS")
			fmt.Printf("  %s\n", wordWrap(runSummary, 68, "  "))
		}
	} else if len(j.ToolsUsed) > 0 {
		sectionJ("TOOLS USED")
		if detail && j.IncidentRunID == "" {
			fmt.Println("  (--detail requires an incident run ID; showing tool list only)")
		}
		for _, t := range j.ToolsUsed {
			fmt.Printf("  • %s\n", t)
		}
	}

	if j.HasMismatch {
		sectionJ("FABRICATION WARNING")
		fmt.Println("  ! One or more delegations reported success but no matching tool")
		fmt.Println("    execution was recorded in the audit trail.")
		fmt.Println("    This may indicate LLM fabrication. Review the agent transcript.")
	}

	if j.IncidentRunID != "" {
		sectionJ("INCIDENT LINK")
		fmt.Printf("  %-18s %s\n", "Run ID:", j.IncidentRunID)
		fmt.Printf("\n  → vault incidents %s\n", j.IncidentRunID)
	}

	// If this is a diagnostic journey (not already a remediation trace), check
	// whether a companion remediation journey exists under the -remed suffix.
	if !strings.HasSuffix(j.TraceID, "-remed") {
		remedTraceID := j.TraceID + "-remed"
		if rems, err := fetchJourneys(gatewayURL, apiKey, map[string]string{
			"trace_id": remedTraceID,
			"limit":    "1",
		}); err == nil && len(rems) > 0 {
			sectionJ("REMEDIATION JOURNEY")
			rem := rems[0]
			fmt.Printf("  %-18s %s\n", "Trace:", rem.TraceID)
			fmt.Printf("  %-18s %s\n", "Agent:", rem.Agent)
			fmt.Printf("  %-18s %s\n", "Outcome:", rem.Outcome)
			if len(rem.ToolsUsed) > 0 {
				fmt.Printf("  %-18s %s\n", "Tools:", strings.Join(rem.ToolsUsed, ", "))
			}
			fmt.Printf("\n  → vault journeys %s\n", rem.TraceID)
		}
	}

	fmt.Println()
}

// ── journey --detail helpers ───────────────────────────────────────────────

// journeyEvent is a minimal mirror of audit.Event for JSON decoding.
type journeyEvent struct {
	EventID   string `json:"event_id"`
	Timestamp string `json:"timestamp"`
	EventType string `json:"event_type"`
	ToolExecution  *journeyToolExec  `json:"tool,omitempty"`
	AgentReasoning *journeyReasoning `json:"agent_reasoning,omitempty"`
}

type journeyToolExec struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

type journeyReasoning struct {
	Reasoning string   `json:"reasoning"`
	ToolCalls []string `json:"tool_calls"`
}

// fetchRunFindings fetches the findings_summary from a single playbook run.
// Returns empty string when not found or on error. This surfaces the agent's
// final FINDINGS line that is stored on the run record rather than as an event.
func fetchRunFindings(gatewayURL, apiKey, runID string) string {
	if gatewayURL == "" || runID == "" {
		return ""
	}
	u := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbook-runs/" + runID
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	var run struct {
		FindingsSummary string `json:"findings_summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return ""
	}
	return run.FindingsSummary
}

// fetchRunEvents calls GET /api/v1/fleet/playbook-runs/{runID}/events and
// returns tool_execution and agent_reasoning events sorted by timestamp.
func fetchRunEvents(gatewayURL, apiKey, runID string) ([]journeyEvent, error) {
	u := strings.TrimSuffix(gatewayURL, "/") +
		"/api/v1/fleet/playbook-runs/" + runID +
		"/events?types=agent_reasoning,tool_execution&limit=500"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var events []journeyEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}

// printReasoningTrace renders agent_reasoning and tool_execution events
// in chronological order, interleaving the model's deliberation text with
// the tool call it preceded.
func printReasoningTrace(events []journeyEvent) {
	if len(events) == 0 {
		fmt.Println("  (no events found)")
		return
	}

	// Track which tool_execution names already had a preceding reasoning block
	// so we can annotate orphan tool calls.
	covered := make(map[string]bool)

	for i, ev := range events {
		switch ev.EventType {
		case "agent_reasoning":
			if ev.AgentReasoning == nil {
				continue
			}
			// Mark the next tool calls as having reasoning.
			for _, tc := range ev.AgentReasoning.ToolCalls {
				covered[tc] = true
			}
			// Print reasoning as a quoted block, wrapping long lines.
			// First line gets an opening quote, last line a closing quote.
			lines := wrapLines(ev.AgentReasoning.Reasoning, 64)
			if len(lines) == 1 {
				fmt.Printf("  \"%s\"\n", lines[0])
			} else {
				for i, line := range lines {
					switch i {
					case 0:
						fmt.Printf("  \"%s\n", line)
					case len(lines) - 1:
						fmt.Printf("   %s\"\n", line)
					default:
						fmt.Printf("   %s\n", line)
					}
				}
			}

		case "tool_execution":
			if ev.ToolExecution == nil {
				continue
			}
			name := ev.ToolExecution.Name
			status := "ok"
			if ev.ToolExecution.Error != "" {
				status = "error"
			}
			fmt.Printf("  ► %-38s [%s]\n", name, status)
			if !covered[name] {
				fmt.Println("    (no preceding reasoning captured)")
			}
		}

		// Blank line between groups (not after the last event).
		if i < len(events)-1 {
			next := events[i+1]
			// Insert blank line before each reasoning block or between tool→reasoning.
			if next.EventType == "agent_reasoning" ||
				(ev.EventType == "tool_execution" && next.EventType == "tool_execution") {
				fmt.Println()
			}
		}
	}
}

// ── vault suggest ─────────────────────────────────────────────────────────

// nextVersion increments the minor component of a dotted version string.
// "1.3" → "1.4", "2" → "2.1", "" → "1.0", "1.3.0" → "1.3.1".
func nextVersion(current string) string {
	if current == "" {
		return "1.0"
	}
	parts := strings.Split(current, ".")
	last := parts[len(parts)-1]
	n, err := strconv.Atoi(last)
	if err != nil {
		return current + ".1"
	}
	parts[len(parts)-1] = strconv.Itoa(n + 1)
	return strings.Join(parts, ".")
}

// ── vault suggest-update ──────────────────────────────────────────────────

// vaultPlaybook is a minimal representation of a gateway playbook for suggest-update.
type vaultPlaybook struct {
	PlaybookID  string `json:"playbook_id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Guidance    string `json:"guidance"`
}

// fetchActivePlaybook retrieves the active playbook for the given series_id from the gateway.
func fetchActivePlaybook(gatewayURL, apiKey, seriesID string) (*vaultPlaybook, error) {
	reqURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks?series_id=" + seriesID
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Playbooks []vaultPlaybook `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Playbooks) == 0 {
		return nil, fmt.Errorf("no playbook found for series %q", seriesID)
	}
	return &result.Playbooks[0], nil
}

// vaultSuggestUpdate fetches the current active playbook for --series-id, then
// calls from-trace with --trace-id to synthesize a proposed update, and displays
// the two side by side so an operator can decide whether to activate the proposal.
func vaultSuggestUpdate(args []string) {
	fs := flag.NewFlagSet("vault suggest-update", flag.ExitOnError)
	var (
		seriesID   string
		traceID    string
		outcome    string
		gatewayURL string
		apiKey     string
	)
	fs.StringVar(&seriesID, "series-id", "", "Series ID of the playbook to update (required)")
	fs.StringVar(&traceID, "trace-id", "", "Run ID or trace ID to synthesize from (auto-selected when omitted)")
	fs.StringVar(&outcome, "outcome", "resolved", "Incident outcome: resolved or escalated")
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if seriesID == "" {
		fmt.Fprintln(os.Stderr, "Error: --series-id is required")
		os.Exit(1)
	}
	// Auto-select the most recent resolved run when --trace-id is omitted.
	if traceID == "" {
		picked, pickErr := pickBestRunForSuggest(gatewayURL, apiKey, seriesID)
		if pickErr != nil {
			fmt.Fprintf(os.Stderr, "Error: --trace-id not provided and could not auto-select a run: %v\n", pickErr)
			fmt.Fprintf(os.Stderr, "Hint: run `faulttest vault incidents %s` to list available runs.\n", seriesID)
			os.Exit(1)
		}
		traceID = picked
		fmt.Printf("Auto-selected trace: %s\n\n", traceID)
	} else if strings.HasPrefix(traceID, "plr_") {
		// Caller supplied a playbook run ID — tool execution events live under the
		// triage journey trace, not the PLR ID itself. Resolve via incident narrative.
		if n, err := fetchIncidentNarrative(gatewayURL, apiKey, traceID); err == nil {
			for _, j := range n.Journeys {
				if j.Phase == "triage" && j.TraceID != "" {
					fmt.Printf("Resolved %s → %s (triage journey)\n\n", traceID, j.TraceID)
					traceID = j.TraceID
					break
				}
			}
		}
	}

	// Step 1: Fetch current active playbook.
	current, err := fetchActivePlaybook(gatewayURL, apiKey, seriesID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching current playbook: %v\n", err)
		os.Exit(1)
	}

	// Step 2: Synthesize proposed update via from-trace.
	reqBody, _ := json.Marshal(map[string]string{
		"trace_id":  traceID,
		"outcome":   outcome,
		"series_id": seriesID,
		"version":   nextVersion(current.Version),
	})
	reqURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/from-trace"
	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error calling from-trace: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, respBody)
		os.Exit(1)
	}
	var traceResult struct {
		Draft      string   `json:"draft"`
		Source     string   `json:"source"`
		PlaybookID string   `json:"playbook_id"`
		Warnings   []string `json:"warnings"`
	}
	if err := json.Unmarshal(respBody, &traceResult); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response: %v\n", err)
		os.Exit(1)
	}

	// Step 3: Display comparison.
	fmt.Printf("=== Playbook Update Proposal: %s ===\n\n", seriesID)
	fmt.Printf("Current:  %s — %s\n", current.PlaybookID, current.Name)
	fmt.Printf("Trace:    %s (outcome: %s)\n\n", traceID, outcome)

	fmt.Println("--- CURRENT DESCRIPTION ---")
	fmt.Println(current.Description)
	if current.Guidance != "" {
		fmt.Println()
		fmt.Println("--- CURRENT GUIDANCE ---")
		fmt.Println(current.Guidance)
	}
	fmt.Println()
	fmt.Println("--- PROPOSED DRAFT (from trace) ---")
	fmt.Println(traceResult.Draft)
	fmt.Println()

	if len(traceResult.Warnings) > 0 {
		fmt.Println("⚠  Protocol warnings — review before activating:")
		for _, w := range traceResult.Warnings {
			fmt.Printf("   • %s\n", w)
		}
		fmt.Println()
	}

	if traceResult.PlaybookID != "" {
		fmt.Printf("Proposed draft saved as: %s (inactive, source=generated)\n\n", traceResult.PlaybookID)
		fmt.Printf("# To activate the proposed draft:\n")
		fmt.Printf("#   curl -X POST %s/api/v1/fleet/playbooks/%s/activate \\\n", gatewayURL, traceResult.PlaybookID)
		fmt.Printf("#        -H 'Authorization: Bearer <key>'\n")
	} else {
		fmt.Printf("# To import this proposal (auditd unavailable for auto-save):\n")
		fmt.Printf("#   curl -X POST %s/api/v1/fleet/playbooks/import \\\n", gatewayURL)
		fmt.Printf("#        -H 'Content-Type: application/json' \\\n")
		fmt.Printf("#        -d '{\"text\": \"<draft YAML above>\", \"format\": \"yaml\", \"hints\": {\"series_id\": \"%s\"}}'\n", seriesID)
	}
}

// ── vault suggest ─────────────────────────────────────────────────────────

// vaultSuggest calls the gateway's from-trace endpoint to synthesize a
// playbook draft from an audit trace, then prints the YAML to stdout.
func vaultSuggest(args []string) {
	fs := flag.NewFlagSet("vault suggest", flag.ExitOnError)
	var (
		traceID    string
		outcome    string
		gatewayURL string
		apiKey     string
	)
	fs.StringVar(&traceID, "trace-id", "", "Audit trace ID to synthesize a playbook from (required)")
	fs.StringVar(&outcome, "outcome", "resolved", "Incident outcome: resolved or escalated")
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if traceID == "" {
		fmt.Fprintln(os.Stderr, "Error: --trace-id is required")
		os.Exit(1)
	}

	reqBody, _ := json.Marshal(map[string]string{
		"trace_id": traceID,
		"outcome":  outcome,
	})
	reqURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/from-trace"

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	var result struct {
		Draft  string `json:"draft"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("# Playbook draft synthesized from trace: %s\n", traceID)
	fmt.Printf("# Source: %s | Outcome: %s\n\n", result.Source, outcome)
	fmt.Println(result.Draft)
	fmt.Println()
	fmt.Println("# To import this playbook:")
	fmt.Printf("#   curl -X POST %s/api/v1/fleet/playbooks/import \\\n", gatewayURL)
	fmt.Println("#     -H 'Content-Type: application/json' \\")
	fmt.Println("#     -d '{\"text\": \"<paste YAML above>\", \"format\": \"yaml\"}'")
}

// ── Evaluation helpers ────────────────────────────────────────────────────

// evaluationResult is the response shape from GET .../evaluation.
type evaluationResult struct {
	RunID            string  `json:"run_id"`
	FailureID        string  `json:"failure_id"`
	KeywordScore     float64 `json:"keyword_score"`
	ToolScore        float64 `json:"tool_score"`
	DiagnosisScore   float64 `json:"diagnosis_score"`
	RemediationScore float64 `json:"remediation_score,omitempty"`
	OverallScore     float64 `json:"overall_score"`
	JudgeUsed        bool    `json:"judge_used,omitempty"`
	Passed           bool    `json:"passed"`
}

// fetchEvaluation retrieves automated evaluation scores for a run from the gateway.
// Returns nil (not an error) when no evaluation has been recorded.
func fetchEvaluation(gatewayURL, apiKey, runID string) *evaluationResult {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbook-runs/" + runID + "/evaluation"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	defer resp.Body.Close()
	var ev evaluationResult
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
		return nil
	}
	if ev.RunID == "" {
		return nil
	}
	return &ev
}

// postEvaluations posts evaluation scores for each result to auditd via the gateway.
// Only results that have a RunID (i.e. the agent call succeeded) are posted.
// Failures are logged but never cause the run to fail.
func postEvaluations(gatewayURL, apiKey string, results []EvalResult) {
	client := &http.Client{Timeout: 10 * time.Second}
	base := strings.TrimSuffix(gatewayURL, "/")
	posted := 0
	for _, r := range results {
		if r.RunID == "" {
			continue
		}
		payload := map[string]any{
			"failure_id":                    r.FailureID,
			"failure_name":                  r.FailureName,
			"keyword_score":                 r.KeywordScore,
			"tool_score":                    r.ToolScore,
			"diagnosis_score":               r.DiagnosisScore,
			"remediation_score":             r.RemediationScore,
			"overall_score":                 r.OverallScore,
			"judge_used":                    !r.JudgeSkipped && r.JudgeModel != "",
			"passed":                        r.Passed,
			"remediation_judge_score":       r.RemediationJudgeScore,
			"remediation_judge_reasoning":   r.RemediationJudgeReasoning,
			"primary_confidence":            r.PrimaryConfidence,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		url := base + "/api/v1/fleet/playbook-runs/" + r.RunID + "/evaluation"
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
			posted++
		}
	}
	if posted > 0 {
		fmt.Printf("Evaluation scores posted to auditd: %d/%d runs\n", posted, len(results))
	}
}

// postStabilityCert posts a fault triage consistency certification to auditd
// via the gateway. Silently skipped when --gateway is not set. Errors are
// logged but never cause the run to fail.
func postStabilityCert(ctx context.Context, cfg *HarnessConfig, f Failure, sr StabilityReport, attr *attributionSummary) {
	if cfg.GatewayURL == "" {
		return
	}
	payload := map[string]any{
		"fault_id":           sr.FailureID,
		"fault_name":         sr.FailureName,
		"playbook_series_id": f.DiagnosisPlaybookSeriesID,
		"diagnosis_model":    cfg.DiagnosisModel, // agent model being certified; from --agent-model / HELPDESK_MODEL_NAME
		"judge_model":        cfg.JudgeModel,     // empty when no judge was used
		"n_runs":             sr.N,
		"pass_rate":          sr.passRate(),
		"conf_range_pp":      int(math.Round(sr.confRange() * 100)),
		"is_stable":          sr.isStable(),
	}
	if attr != nil {
		payload["primary_attribution"] = attr.PrimaryAttribution
		payload["attribution_consistent"] = attr.AttributionConsistent
		payload["attribution_distribution"] = attr.AttributionDistribution
		payload["judge_spread"] = attr.JudgeSpread
		payload["taxonomy_version"] = attr.TaxonomyVersion
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("fault stability: failed to marshal cert", "fault_id", f.ID, "err", err)
		return
	}
	url := strings.TrimSuffix(cfg.GatewayURL, "/") + "/api/v1/fleet/fault-stability"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("fault stability: failed to build request", "fault_id", f.ID, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GatewayAPIKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("fault stability: POST failed", "fault_id", f.ID, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		slog.Warn("fault stability: unexpected status", "fault_id", f.ID, "status", resp.StatusCode)
		return
	}
	verdict := "UNSTABLE"
	if sr.isStable() {
		verdict = "STABLE"
	}
	slog.Info("fault stability cert posted", "fault_id", f.ID, "verdict", verdict, "n_runs", sr.N)
}

// ── vault versions ────────────────────────────────────────────────────────────

// versionStats mirrors the PlaybookVersionStats struct returned by the gateway.
type versionStats struct {
	SeriesID        string  `json:"series_id"`
	PlaybookID      string  `json:"playbook_id"`
	OriginTrace     string  `json:"origin_trace"`
	Version         string  `json:"version"`
	IsActive        bool    `json:"is_active"`
	TotalRuns       int     `json:"total_runs"`
	Resolved        int     `json:"resolved"`
	ResolutionRate  float64 `json:"resolution_rate"`
	Transitioned    int     `json:"transitioned"`
	TransitionRate  float64 `json:"transition_rate"`
	AvgStepCount    float64 `json:"avg_step_count"`
	AvgRecoverySecs float64 `json:"avg_recovery_secs"`
	AvgDiagnosisScore   float64 `json:"avg_diagnosis_score"`
	DiagEvalCount       int     `json:"diag_eval_count"`
	AvgRemediationScore float64 `json:"avg_remediation_score"`
	RemedEvalCount      int     `json:"remed_eval_count"`
	RemFeedbackCount    int     `json:"rem_feedback_count"`
	RemFeedbackRate     float64 `json:"rem_feedback_rate"`

	JudgeVerdict string `json:"judge_verdict,omitempty"`
	JudgeModel   string `json:"judge_model,omitempty"`
	JudgeAt      string `json:"judge_at,omitempty"`
}

// fetchVersionStats calls GET /api/v1/fleet/series/{seriesID}/version-stats.
// Returns nil on network error or non-200 response.
func fetchVersionStats(gatewayURL, apiKey, seriesID string) ([]versionStats, error) {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/series/" + seriesID + "/version-stats"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d", resp.StatusCode)
	}
	var body struct {
		Versions []versionStats `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	versions := body.Versions

	// Append pending drafts that have a judge verdict but no runs yet.
	// These show in vault versions as a preview of the next activation.
	draftURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks?active_only=false&series_id=" + seriesID
	draftReq, _ := http.NewRequest(http.MethodGet, draftURL, nil)
	if apiKey != "" {
		draftReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if draftResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(draftReq); err == nil {
		defer draftResp.Body.Close()
		var drafts struct {
			Playbooks []struct {
				PlaybookID   string `json:"playbook_id"`
				Version      string `json:"version"`
				IsActive     bool   `json:"is_active"`
				JudgeVerdict string `json:"judge_verdict"`
				JudgeModel   string `json:"judge_model"`
				JudgeAt      string `json:"judge_at"`
			} `json:"playbooks"`
		}
		if json.NewDecoder(draftResp.Body).Decode(&drafts) == nil {
			// Index playbooks already in the stats slice so we don't double-add.
			inStats := map[string]bool{}
			for _, v := range versions {
				if v.PlaybookID != "" {
					inStats[v.PlaybookID] = true
				}
			}
			for _, d := range drafts.Playbooks {
				if d.JudgeVerdict == "" || inStats[d.PlaybookID] {
					continue
				}
				ver := d.Version
				if ver == "" {
					if d.IsActive {
						ver = "(active, no runs)"
					} else {
						ver = "(pending)"
					}
				}
				versions = append(versions, versionStats{
					PlaybookID:   d.PlaybookID,
					Version:      ver,
					IsActive:     d.IsActive,
					JudgeVerdict: d.JudgeVerdict,
					JudgeModel:   d.JudgeModel,
					JudgeAt:      d.JudgeAt,
				})
			}
		}
	}

	return versions, nil
}

// formatDuration formats seconds as a compact string: 42s / 1m23s / 1h5m.
func formatDuration(secs float64) string {
	if secs <= 0 {
		return "–"
	}
	d := time.Duration(secs * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// printVersionTable prints a formatted per-version stats table for one series.
// label is printed as a section header (e.g. "TRIAGE  pbs_connection_triage").
func printVersionTable(label string, versions []versionStats) {
	const (
		colVer      = 10
		colRuns     = 6
		colSuccess  = 9
		colSteps    = 10
		colTime     = 10
		colDiag     = 9
		colRemed    = 9
		colApproach = 9
		colJudge    = 12
	)
	sepWidth := colVer + 2 + colRuns + 2 + colSuccess + 2 + colSteps + 2 + colTime + 2 + colDiag + 2 + colRemed + 2 + colApproach + 2 + colJudge

	if label != "" {
		fmt.Printf("%s  (%d version(s))\n", label, len(versions))
	}
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colVer, "VERSION", colRuns, "RUNS", colSuccess, "SUCCESS%",
		colSteps, "AVG STEPS", colTime, "AVG TIME", colDiag, "AVG DIAG",
		colRemed, "AVG REMED", colApproach, "APPROACH OK", "JUDGE VERDICT")
	fmt.Println(strings.Repeat("─", sepWidth))

	for _, v := range versions {
		ver := v.Version
		if v.IsActive {
			ver += " *"
		}

		successStr := "–"
		if v.TotalRuns > 0 {
			// SUCCESS% = resolved + transitioned: covers both "fixed it" (remediation)
			// and "handed off successfully" (triage) in one metric.
			rate := v.ResolutionRate + v.TransitionRate
			successStr = fmt.Sprintf("%d%%", int(rate*100))
		}

		stepsStr := "–"
		if v.AvgStepCount > 0 {
			stepsStr = fmt.Sprintf("%.1f", v.AvgStepCount)
		}

		timeStr := formatDuration(v.AvgRecoverySecs)

		diagStr := "–"
		if v.DiagEvalCount > 0 {
			diagStr = fmt.Sprintf("%d%%", int(v.AvgDiagnosisScore*100))
		}

		remedStr := "–"
		if v.RemedEvalCount > 0 {
			remedStr = fmt.Sprintf("%d%%", int(v.AvgRemediationScore*100))
		}

		approachStr := "–"
		if v.RemFeedbackCount > 0 {
			approachStr = fmt.Sprintf("%d%%", int(v.RemFeedbackRate*100))
		}

		judgeStr := "–"
		if v.JudgeVerdict != "" {
			judgeStr = v.JudgeVerdict
		}

		fmt.Printf("%-*s  %-*d  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			colVer, ver,
			colRuns, v.TotalRuns,
			colSuccess, successStr,
			colSteps, stepsStr,
			colTime, timeStr,
			colDiag, diagStr,
			colRemed, remedStr,
			colApproach, approachStr,
			judgeStr,
		)
		if v.PlaybookID != "" || v.OriginTrace != "" {
			detail := "  id=" + v.PlaybookID
			if v.OriginTrace != "" {
				detail += "  from=" + v.OriginTrace
			}
			if v.JudgeModel != "" {
				detail += "  judge=" + v.JudgeModel
			}
			fmt.Println(detail)
		}
	}
	fmt.Println()
}

// vaultVersions shows per-version run stats for a playbook series or fault.
//
// When given a fault ID (e.g. db-max-connections), shows both the triage and
// remediation series side-by-side so the full incident pipeline is visible.
// When given a series ID (pbs_* prefix), shows just that series.
func vaultVersions(args []string) {
	fs := flag.NewFlagSet("vault versions", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault versions")
		os.Exit(1)
	}
	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault versions <fault-id or series-id> [--gateway ...] [--api-key ...]")
		os.Exit(1)
	}

	arg := fs.Args()[0]

	// Series ID passed directly — single-series mode.
	if strings.HasPrefix(arg, "pbs_") {
		versions, err := fetchVersionStats(cfg.GatewayURL, cfg.GatewayAPIKey, arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching version stats: %v\n", err)
			os.Exit(1)
		}
		if len(versions) == 0 {
			fmt.Printf("No run history found for series %q.\n", arg)
			return
		}
		fmt.Printf("Version stats for %s\n\n", arg)
		printVersionTable("", versions)
		fmt.Println("* = currently active   SUCCESS% = resolved + transitioned")
		fmt.Println("id/from lines show playbook_id and the run that generated that version")
		return
	}

	// Fault ID passed — resolve both triage and remediation series.
	cat, err := loadActiveCatalog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
		os.Exit(1)
	}
	var triageSeries, remediationSeries, faultName string
	var faultFound bool
	for _, f := range cat.Failures {
		if f.ID == arg {
			triageSeries = f.DiagnosisPlaybookSeriesID
			remediationSeries = f.Remediation.PlaybookID
			faultName = f.Name
			faultFound = true
			break
		}
	}
	if !faultFound {
		fmt.Fprintf(os.Stderr, "Unknown fault ID %q. Run `faulttest list` to see available faults.\n", arg)
		os.Exit(1)
	}

	if triageSeries == "" && remediationSeries == "" {
		fmt.Fprintf(os.Stderr, "Fault %q has no triage or remediation playbook series.\n", arg)
		os.Exit(1)
	}

	fmt.Printf("Version stats for fault: %s (%s)\n\n", arg, faultName)

	hasAny := false
	if triageSeries != "" {
		versions, err := fetchVersionStats(cfg.GatewayURL, cfg.GatewayAPIKey, triageSeries)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch triage stats for %s: %v\n", triageSeries, err)
		} else if len(versions) > 0 {
			printVersionTable("TRIAGE  "+triageSeries, versions)
			hasAny = true
		} else {
			fmt.Printf("TRIAGE  %s — no run history yet\n\n", triageSeries)
		}
	}

	if remediationSeries != "" {
		versions, err := fetchVersionStats(cfg.GatewayURL, cfg.GatewayAPIKey, remediationSeries)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch remediation stats for %s: %v\n", remediationSeries, err)
		} else if len(versions) > 0 {
			printVersionTable("REMEDIATION  "+remediationSeries, versions)
			hasAny = true
		} else {
			fmt.Printf("REMEDIATION  %s — no run history yet\n\n", remediationSeries)
		}
	}

	if hasAny {
		fmt.Println("* = currently active   SUCCESS% = resolved + transitioned")
		fmt.Println("id/from lines show playbook_id and the run that generated that version")
	}
}

// ── vault judge-accuracy ───────────────────────────────────────────────────

// vaultJudgeAccuracy shows, per series, the judge verdict recorded at diff time
// and the actual outcome after runs accumulated on that version.  This closes the
// loop: "the judge said LIKELY_IMPROVEMENT — did run data confirm it?"
func vaultJudgeAccuracy(args []string) {
	fs := flag.NewFlagSet("vault judge-accuracy", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault judge-accuracy")
		os.Exit(1)
	}

	// Resolve series IDs: if a fault/series ID was given use it, otherwise fall
	// back to listing all playbooks and collecting unique series IDs.
	var seriesIDs []string
	if len(fs.Args()) > 0 {
		arg := fs.Args()[0]
		if strings.HasPrefix(arg, "pbs_") {
			seriesIDs = []string{arg}
		} else {
			// Treat as fault ID — resolve via catalog.
			cat, err := loadActiveCatalog(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
				os.Exit(1)
			}
			for _, f := range cat.Failures {
				if f.ID == arg {
					if f.DiagnosisPlaybookSeriesID != "" {
						seriesIDs = append(seriesIDs, f.DiagnosisPlaybookSeriesID)
					}
					if f.Remediation.PlaybookID != "" {
						seriesIDs = append(seriesIDs, f.Remediation.PlaybookID)
					}
					break
				}
			}
			if len(seriesIDs) == 0 {
				fmt.Fprintf(os.Stderr, "Unknown fault %q or no series found.\n", arg)
				os.Exit(1)
			}
		}
	} else {
		// No filter — collect series IDs from all playbooks that have judge verdicts.
		listURL := strings.TrimSuffix(cfg.GatewayURL, "/") + "/api/v1/fleet/playbooks?active_only=false"
		listReq, _ := http.NewRequest(http.MethodGet, listURL, nil)
		if cfg.GatewayAPIKey != "" {
			listReq.Header.Set("Authorization", "Bearer "+cfg.GatewayAPIKey)
		}
		listResp, err := (&http.Client{Timeout: 15 * time.Second}).Do(listReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching playbooks: %v\n", err)
			os.Exit(1)
		}
		defer listResp.Body.Close()
		var listBody struct {
			Playbooks []struct {
				SeriesID     string `json:"series_id"`
				JudgeVerdict string `json:"judge_verdict"`
			} `json:"playbooks"`
		}
		if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
			fmt.Fprintf(os.Stderr, "Error decoding playbook list: %v\n", err)
			os.Exit(1)
		}
		seen := map[string]bool{}
		for _, pb := range listBody.Playbooks {
			if pb.SeriesID != "" && pb.JudgeVerdict != "" && !seen[pb.SeriesID] {
				seen[pb.SeriesID] = true
				seriesIDs = append(seriesIDs, pb.SeriesID)
			}
		}
		if len(seriesIDs) == 0 {
			fmt.Println("No judge verdicts recorded yet. Run `faulttest vault diff` on a draft to generate them.")
			return
		}
	}

	type judgeRow struct {
		SeriesID     string
		Version      string
		JudgeVerdict string
		JudgeModel   string
		JudgeAt      string
		TotalRuns    int
		SuccessRate  float64
	}

	// Build rows by reading judge verdicts directly from each playbook in the
	// series — this includes drafts (no runs yet). Run stats are fetched
	// separately and merged by playbook_id so unactivated drafts show TotalRuns=0.
	var rows []judgeRow
	for _, sid := range seriesIDs {
		// Fetch all playbooks in this series (active and draft) to read verdicts.
		pbURL := strings.TrimSuffix(cfg.GatewayURL, "/") + "/api/v1/fleet/playbooks?active_only=false&series_id=" + sid
		pbReq, _ := http.NewRequest(http.MethodGet, pbURL, nil)
		if cfg.GatewayAPIKey != "" {
			pbReq.Header.Set("Authorization", "Bearer "+cfg.GatewayAPIKey)
		}
		pbResp, err := (&http.Client{Timeout: 15 * time.Second}).Do(pbReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch playbooks for %s: %v\n", sid, err)
			continue
		}
		var pbBody struct {
			Playbooks []struct {
				PlaybookID   string `json:"playbook_id"`
				Version      string `json:"version"`
				JudgeVerdict string `json:"judge_verdict"`
				JudgeModel   string `json:"judge_model"`
				JudgeAt      string `json:"judge_at"`
			} `json:"playbooks"`
		}
		if err := json.NewDecoder(pbResp.Body).Decode(&pbBody); err != nil {
			pbResp.Body.Close()
			continue
		}
		pbResp.Body.Close()

		// Build a map of playbook_id → run stats for enrichment.
		runsByPB := map[string]versionStats{}
		if versions, err := fetchVersionStats(cfg.GatewayURL, cfg.GatewayAPIKey, sid); err == nil {
			for _, v := range versions {
				runsByPB[v.PlaybookID] = v
			}
		}

		for _, pb := range pbBody.Playbooks {
			if pb.JudgeVerdict == "" {
				continue
			}
			row := judgeRow{
				SeriesID:     sid,
				Version:      pb.Version,
				JudgeVerdict: pb.JudgeVerdict,
				JudgeModel:   pb.JudgeModel,
				JudgeAt:      pb.JudgeAt,
			}
			if v, ok := runsByPB[pb.PlaybookID]; ok {
				row.TotalRuns = v.TotalRuns
				row.SuccessRate = v.ResolutionRate + v.TransitionRate
			}
			rows = append(rows, row)
		}
	}

	if len(rows) == 0 {
		fmt.Println("No judge verdicts found for the specified series.")
		return
	}

	const (
		colSeries  = 32
		colVer     = 10
		colVerdict = 20
		colRuns    = 6
		colSuccess = 9
	)
	sepWidth := colSeries + 2 + colVer + 2 + colVerdict + 2 + colRuns + 2 + colSuccess + 2 + 20
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colSeries, "SERIES", colVer, "VERSION", colVerdict, "JUDGE VERDICT",
		colRuns, "RUNS", colSuccess, "SUCCESS%", "JUDGE MODEL")
	fmt.Println(strings.Repeat("─", sepWidth))
	for _, r := range rows {
		successStr := "–"
		if r.TotalRuns > 0 {
			successStr = fmt.Sprintf("%d%%", int(r.SuccessRate*100))
		}
		series := r.SeriesID
		if len(series) > colSeries {
			series = series[:colSeries-1] + "…"
		}
		fmt.Printf("%-*s  %-*s  %-*s  %-*d  %-*s  %s\n",
			colSeries, series,
			colVer, r.Version,
			colVerdict, r.JudgeVerdict,
			colRuns, r.TotalRuns,
			colSuccess, successStr,
			r.JudgeModel,
		)
	}
	fmt.Println()
	fmt.Println("JUDGE VERDICT is the prediction recorded by `vault diff`.")
	fmt.Println("SUCCESS% is the actual outcome after runs on this version.")
}

// ── vault cert-compare ────────────────────────────────────────────────────

// certCompareRow holds the per-fault data for one row in the comparison table.
type certCompareRow struct {
	faultID   string
	faultName string
	oldCert   *certCompareEntry // nil = not run under old model
	newCert   *certCompareEntry // nil = not run under new model
}

type certCompareEntry struct {
	isStable              bool
	passRate              float64
	nRuns                 int
	testedAt              string
	primaryAttribution    string
	attributionConsistent bool
	taxonomyVersion       string
	judgeSpread           float64
}

// vaultCertCompare compares stability certs for two diagnosis models.
// Usage: faulttest vault cert-compare <model-a> <model-b> [--gateway ...] [--api-key ...]
func vaultCertCompare(args []string) {
	fs := flag.NewFlagSet("vault cert-compare", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	positional := fs.Args()
	if len(positional) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault cert-compare <model-a> <model-b> [--gateway URL] [--api-key KEY]")
		fmt.Fprintln(os.Stderr, "  model-a  baseline model (e.g. claude-sonnet-4-5)")
		fmt.Fprintln(os.Stderr, "  model-b  candidate model (e.g. claude-sonnet-4-6)")
		os.Exit(1)
	}
	modelA := positional[0]
	modelB := positional[1]

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault cert-compare")
		os.Exit(1)
	}

	// Fetch all certs (every fault × model row).
	allCerts, err := fetchAllStabilityCerts(cfg.GatewayURL, cfg.GatewayAPIKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching stability certs: %v\n", err)
		os.Exit(1)
	}

	// Index by (fault_id, diagnosis_model).
	type certKey struct{ faultID, model string }
	byKey := make(map[certKey]*certCompareEntry)
	names := make(map[string]string) // fault_id → fault_name
	for _, c := range allCerts {
		byKey[certKey{c.FaultID, c.DiagnosisModel}] = &certCompareEntry{
			isStable:              c.IsStable,
			passRate:              c.PassRate,
			nRuns:                 c.NRuns,
			testedAt:              c.TestedAt,
			primaryAttribution:    c.PrimaryAttribution,
			attributionConsistent: c.AttributionConsistent,
			taxonomyVersion:       c.TaxonomyVersion,
			judgeSpread:           c.JudgeSpread,
		}
		if c.FaultName != "" {
			names[c.FaultID] = c.FaultName
		} else {
			names[c.FaultID] = c.FaultID
		}
	}

	// Collect all fault IDs that appear under either model.
	seen := make(map[string]bool)
	for k := range byKey {
		if k.model == modelA || k.model == modelB {
			seen[k.faultID] = true
		}
	}
	if len(seen) == 0 {
		fmt.Printf("No stability certs found for models %q or %q.\n", modelA, modelB)
		fmt.Println("Run `faulttest run --repeat N` with --agent-model set to each model to generate certs.")
		return
	}

	// Build sorted rows.
	faultIDs := make([]string, 0, len(seen))
	for id := range seen {
		faultIDs = append(faultIDs, id)
	}
	sort.Strings(faultIDs)

	rows := make([]certCompareRow, 0, len(faultIDs))
	for _, id := range faultIDs {
		rows = append(rows, certCompareRow{
			faultID:   id,
			faultName: names[id],
			oldCert:   byKey[certKey{id, modelA}],
			newCert:   byKey[certKey{id, modelB}],
		})
	}

	// Sort: regressions first, then improvements, then unchanged, then missing.
	sort.SliceStable(rows, func(i, j int) bool {
		return changeOrder(rows[i]) < changeOrder(rows[j])
	})

	// Tally.
	var nRegression, nImprovement, nUnchanged, nMissing, nMatchedStable int
	for _, r := range rows {
		switch changeLabel(r) {
		case "⚠ REGRESSION":
			nRegression++
		case "✓ IMPROVEMENT":
			nImprovement++
		case "—":
			nUnchanged++
			if r.oldCert != nil && r.oldCert.isStable {
				nMatchedStable++
			}
		default:
			nMissing++
		}
	}

	// Shorten model names for column headers (last two dash-separated segments).
	shortA := shortModelName(modelA)
	shortB := shortModelName(modelB)

	const (
		colFault  = 34
		colOld    = 12
		colNew    = 12
		colChange = 16
	)

	fmt.Printf("Stability comparison: %s → %s\n", modelA, modelB)
	fmt.Printf("STABLE/UNSTABLE = triage diagnosis cert only (keyword + tool + category scoring across --repeat runs; remediation not included)\n\n")
	fmt.Printf("%-*s  %-*s  %-*s  %s\n",
		colFault, "FAULT",
		colOld, shortA,
		colNew, shortB,
		"CHANGE",
	)
	fmt.Println(strings.Repeat("─", colFault+2+colOld+2+colNew+2+colChange))

	for _, r := range rows {
		oldStr := certStatusStr(r.oldCert)
		newStr := certStatusStr(r.newCert)
		change := changeLabel(r)
		fmt.Printf("%-*s  %-*s  %-*s  %s\n",
			colFault, truncate(r.faultName, colFault),
			colOld, oldStr,
			colNew, newStr,
			change,
		)
		// For regressions: show diagnosis-rate delta and attribution context.
		if r.oldCert != nil && r.newCert != nil && r.oldCert.isStable && !r.newCert.isStable {
			delta := r.newCert.passRate - r.oldCert.passRate
			fmt.Printf("  %s diag_rate: %.0f%% → %.0f%%  (Δ%+.0f%%)\n",
				r.faultID, r.oldCert.passRate*100, r.newCert.passRate*100, delta*100)
		}
		// Attribution comparison when both certs have taxonomy data.
		if r.oldCert != nil && r.newCert != nil &&
			r.oldCert.primaryAttribution != "" && r.newCert.primaryAttribution != "" {
			oldTV := r.oldCert.taxonomyVersion
			newTV := r.newCert.taxonomyVersion
			taxWarning := ""
			if oldTV != "" && newTV != "" && majorVersion(oldTV) != majorVersion(newTV) {
				taxWarning = fmt.Sprintf("  ⚠ TAXONOMY MAJOR %s→%s: attribution comparison invalid", oldTV, newTV)
			}
			oldAttr := r.oldCert.primaryAttribution
			if !r.oldCert.attributionConsistent {
				oldAttr += "(split)"
			}
			newAttr := r.newCert.primaryAttribution
			if !r.newCert.attributionConsistent {
				newAttr += "(split)"
			}
			if oldAttr != newAttr || taxWarning != "" {
				fmt.Printf("  attribution: %s → %s%s\n", oldAttr, newAttr, taxWarning)
			}
		}
	}

	fmt.Println()
	total := len(rows)
	fmt.Printf("%d fault(s) total", total)
	if nRegression > 0 {
		fmt.Printf("  ·  %d regression(s) ⚠", nRegression)
	}
	if nImprovement > 0 {
		fmt.Printf("  ·  %d improvement(s) ✓", nImprovement)
	}
	if nUnchanged > 0 {
		fmt.Printf("  ·  %d unchanged", nUnchanged)
	}
	if nMissing > 0 {
		fmt.Printf("  ·  %d not run under both models ?", nMissing)
	}
	fmt.Println()

	if nRegression > 0 {
		fmt.Printf("\n⚠  %d regression(s) detected — promote %s only after investigating the faults above.\n", nRegression, modelB)
	} else if nMissing > 0 {
		fmt.Printf("\n?  %d fault(s) not yet certified under both models — complete the cert suite before promoting.\n", nMissing)
	} else {
		fmt.Printf("\n✓  No regressions. %s is cert-equivalent to %s across all %d fault(s).\n", modelB, modelA, nMatchedStable+nImprovement)
	}
}

// fetchAllStabilityCerts retrieves every (fault, model) cert row from the gateway.
func fetchAllStabilityCerts(gatewayURL, apiKey string) ([]struct {
	FaultID               string  `json:"fault_id"`
	FaultName             string  `json:"fault_name"`
	DiagnosisModel        string  `json:"diagnosis_model"`
	NRuns                 int     `json:"n_runs"`
	PassRate              float64 `json:"pass_rate"`
	IsStable              bool    `json:"is_stable"`
	TestedAt              string  `json:"tested_at"`
	PrimaryAttribution    string  `json:"primary_attribution"`
	AttributionConsistent bool    `json:"attribution_consistent"`
	TaxonomyVersion       string  `json:"taxonomy_version"`
	JudgeSpread           float64 `json:"judge_spread"`
}, error) {
	u := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/fault-stability"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Certs []struct {
			FaultID               string  `json:"fault_id"`
			FaultName             string  `json:"fault_name"`
			DiagnosisModel        string  `json:"diagnosis_model"`
			NRuns                 int     `json:"n_runs"`
			PassRate              float64 `json:"pass_rate"`
			IsStable              bool    `json:"is_stable"`
			TestedAt              string  `json:"tested_at"`
			PrimaryAttribution    string  `json:"primary_attribution"`
			AttributionConsistent bool    `json:"attribution_consistent"`
			TaxonomyVersion       string  `json:"taxonomy_version"`
			JudgeSpread           float64 `json:"judge_spread"`
		} `json:"certs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return result.Certs, nil
}

func certStatusStr(e *certCompareEntry) string {
	if e == nil {
		return "(no data)"
	}
	if e.isStable {
		return "STABLE"
	}
	return "UNSTABLE"
}

func changeLabel(r certCompareRow) string {
	if r.oldCert == nil || r.newCert == nil {
		return "? NOT RUN YET"
	}
	if r.oldCert.isStable && !r.newCert.isStable {
		return "⚠ REGRESSION"
	}
	if !r.oldCert.isStable && r.newCert.isStable {
		return "✓ IMPROVEMENT"
	}
	return "—"
}

// changeOrder returns a sort key: regressions first, then improvements, then stable, then unstable, then missing.
func changeOrder(r certCompareRow) int {
	switch changeLabel(r) {
	case "⚠ REGRESSION":
		return 0
	case "✓ IMPROVEMENT":
		return 1
	case "—":
		if r.oldCert != nil && r.oldCert.isStable {
			return 2 // stable/stable unchanged
		}
		return 3 // unstable/unstable unchanged
	default:
		return 4 // missing
	}
}

// majorVersion extracts the major component of a semver string "MAJOR.minor".
// Returns the whole string when no dot is present.
func majorVersion(v string) string {
	if i := strings.IndexByte(v, '.'); i > 0 {
		return v[:i]
	}
	return v
}

// shortModelName strips the leading "claude-" vendor prefix if present,
// e.g. "claude-sonnet-4-5" → "sonnet-4-5". Returns the name unchanged otherwise.
func shortModelName(model string) string {
	if after, ok := strings.CutPrefix(model, "claude-"); ok {
		return after
	}
	return model
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ── vault calibration ──────────────────────────────────────────────────────

// calibrationBand mirrors audit.CalibrationBand returned by the gateway.
type calibrationBand struct {
	Band           string  `json:"band"`
	Runs           int     `json:"runs"`
	Correct        int     `json:"correct"`
	ActualAccuracy float64 `json:"actual_accuracy"`
	Calibration    string  `json:"calibration"`
	HeuristicRuns  int     `json:"heuristic_runs,omitempty"`
}

// calibrationReport mirrors audit.CalibrationReport.
type calibrationReport struct {
	SeriesID         string            `json:"series_id,omitempty"`
	Bands            []calibrationBand `json:"bands"`
	TotalRuns        int               `json:"total_runs"`
	RemediationBands []calibrationBand `json:"remediation_bands,omitempty"`
	RemediationRuns  int               `json:"remediation_runs"`
	HeuristicCount   int               `json:"heuristic_count,omitempty"`
	HumanRuns        int               `json:"human_runs,omitempty"`
	AutoJudgeRuns    int               `json:"auto_judge_runs,omitempty"`
}

// fetchCalibration calls GET /api/v1/fleet/calibration[?series_id=...].
func fetchCalibration(gatewayURL, apiKey, seriesID string) (*calibrationReport, error) {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/calibration"
	if seriesID != "" {
		url += "?series_id=" + seriesID
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d", resp.StatusCode)
	}
	var report calibrationReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, err
	}
	return &report, nil
}

// ── Incident journey (deep-dive) ─────────────────────────────────────────────

// narrativeFeedback is one feedback record within the incident narrative response.
type narrativeFeedback struct {
	RunID          string `json:"run_id"`
	FeedbackType   string `json:"feedback_type"`
	FeedbackTime   string `json:"feedback_time"`
	VerdictCorrect *bool  `json:"verdict_correct"`
	VerdictNotes   string `json:"verdict_notes"`
	Operator       string `json:"operator"`
}

// narrativeEval mirrors RunEvaluation fields used by the deep-dive display.
type narrativeEval struct {
	DiagnosisScore            float64 `json:"diagnosis_score"`
	RemediationScore          float64 `json:"remediation_score,omitempty"`
	OverallScore              float64 `json:"overall_score"`
	RemediationJudgeScore     float64 `json:"remediation_judge_score"`
	RemediationJudgeReasoning string  `json:"remediation_judge_reasoning"`
	PrimaryConfidence         float64 `json:"primary_confidence"`
	JudgeUsed                 bool    `json:"judge_used"`
}

// narrativeHypothesis mirrors audit.DiagnosticHypothesis.
type narrativeHypothesis struct {
	Rank           int     `json:"rank"`
	Text           string  `json:"text"`
	Confidence     float64 `json:"confidence"`
	Evidence       string  `json:"evidence,omitempty"`
	RejectedReason string  `json:"rejected_reason,omitempty"`
	IsPrimary      bool    `json:"is_primary"`
}

// narrativeDiagReport mirrors audit.DiagnosticReport.
type narrativeDiagReport struct {
	Hypotheses []narrativeHypothesis `json:"hypotheses"`
}

// narrativeStep mirrors audit.PlaybookRunStep.
type narrativeStep struct {
	StepName string `json:"step_name"`
	Status   string `json:"status"`
}

// narrativeJourneyRef mirrors audit.IncidentJourneyRef for JSON decoding.
type narrativeJourneyRef struct {
	Phase   string `json:"phase"`
	TraceID string `json:"trace_id"`
}

// incidentNarrative mirrors gateway.IncidentNarrative for JSON decoding.
type incidentNarrative struct {
	IncidentID     string     `json:"incident_id"`
	StartedAt      time.Time  `json:"started_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	DurationSec    float64    `json:"duration_sec,omitempty"`
	Operator       string     `json:"operator"`
	TriggerContext string     `json:"trigger_context,omitempty"`
	Triage      struct {
		RunID            string               `json:"run_id"`
		Playbook         string               `json:"playbook"`
		Findings         string               `json:"findings,omitempty"`
		DiagnosticReport *narrativeDiagReport `json:"diagnostic_report,omitempty"`
	} `json:"triage"`
	Gate *struct {
		ApprovedBy     string    `json:"approved_by,omitempty"`
		AcknowledgedAt time.Time `json:"acknowledged_at,omitempty"`
		Resolution     string    `json:"resolution"`
		Reason         string    `json:"reason,omitempty"`
	} `json:"gate,omitempty"`
	Remediation *struct {
		RunID      string          `json:"run_id"`
		Playbook   string          `json:"playbook"`
		Outcome    string          `json:"outcome"`
		Findings   string          `json:"findings,omitempty"`
		Transcript string          `json:"transcript,omitempty"`
		Steps      []narrativeStep `json:"steps,omitempty"`
	} `json:"remediation,omitempty"`
	Feedback   []narrativeFeedback   `json:"feedback,omitempty"`
	Evaluation *narrativeEval        `json:"evaluation,omitempty"`
	Journeys   []narrativeJourneyRef `json:"journeys,omitempty"`
}

func fetchIncidentNarrative(gatewayURL, apiKey, runID string) (*incidentNarrative, error) {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/incidents/" + runID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("incident %s not found", runID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var n incidentNarrative
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &n, nil
}

func printIncidentJourney(gatewayURL, apiKey, runID string) {
	n, err := fetchIncidentNarrative(gatewayURL, apiKey, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	divider := strings.Repeat("═", 60)
	section := func(title string) {
		fmt.Printf("\n── %-54s\n", title)
	}

	fmt.Printf("\n%s\n", divider)
	started := n.StartedAt.UTC().Format("2006-01-02 15:04 UTC")
	if n.DurationSec > 0 {
		fmt.Printf("INCIDENT %s\nStarted: %s   Duration: %.0fs\n", n.IncidentID, started, n.DurationSec)
	} else {
		fmt.Printf("INCIDENT %s\nStarted: %s\n", n.IncidentID, started)
	}
	if n.Operator != "" {
		fmt.Printf("Operator: %s\n", n.Operator)
	}
	if n.TriggerContext != "" {
		fmt.Printf("Triggered by: %s\n", wordWrap(n.TriggerContext, 70, "              "))
	}
	fmt.Printf("%s\n", divider)

	// ── TRIAGE ───────────────────────────────────────────────
	section("TRIAGE")
	fmt.Printf("Playbook:  %s\n", n.Triage.Playbook)
	if n.Triage.Findings != "" {
		fmt.Printf("Findings:  %s\n", wordWrap(n.Triage.Findings, 70, "           "))
	}
	if n.Triage.DiagnosticReport != nil && len(n.Triage.DiagnosticReport.Hypotheses) > 0 {
		fmt.Println("\nHypotheses:")
		for _, h := range n.Triage.DiagnosticReport.Hypotheses {
			tag := fmt.Sprintf("[REJECTED %2.0f%%]", h.Confidence*100)
			if h.IsPrimary {
				tag = fmt.Sprintf("[PRIMARY  %2.0f%%]", h.Confidence*100)
			}
			fmt.Printf("  %s %s\n", tag, h.Text)
			if h.Evidence != "" {
				fmt.Printf("  %s Evidence: %q\n", strings.Repeat(" ", len(tag)), h.Evidence)
			}
			if h.RejectedReason != "" {
				fmt.Printf("  %s Rejected: %s\n", strings.Repeat(" ", len(tag)), h.RejectedReason)
			}
		}
	}

	// ── GATE ─────────────────────────────────────────────────
	if n.Gate != nil {
		section("GATE")
		gateLine := n.Gate.Resolution
		if n.Gate.ApprovedBy != "" {
			gateLine = fmt.Sprintf("%s by %s", n.Gate.Resolution, n.Gate.ApprovedBy)
		}
		if !n.Gate.AcknowledgedAt.IsZero() {
			gateLine += "  at " + n.Gate.AcknowledgedAt.UTC().Format("15:04 UTC")
		}
		fmt.Printf("Decision:  %s\n", gateLine)
		// At-gate feedback
		var gateFeedback []string
		for _, fb := range n.Feedback {
			if fb.FeedbackTime != "at_gate" {
				continue
			}
			verdict := "–"
			if fb.VerdictCorrect != nil {
				if *fb.VerdictCorrect {
					verdict = "✓ correct"
				} else {
					verdict = "✗ wrong"
				}
			}
			label := fb.FeedbackType + " at gate"
			if fb.VerdictNotes != "" {
				verdict += fmt.Sprintf(" (%s)", fb.VerdictNotes)
			}
			gateFeedback = append(gateFeedback, fmt.Sprintf("  %-28s %s", label+":", verdict))
		}
		if len(gateFeedback) > 0 {
			fmt.Println("Feedback:")
			for _, f := range gateFeedback {
				fmt.Println(f)
			}
		}
	}

	// ── REMEDIATION ──────────────────────────────────────────
	if n.Remediation != nil {
		section("REMEDIATION")
		fmt.Printf("Playbook:  %s   Outcome: %s\n", n.Remediation.Playbook, n.Remediation.Outcome)
		if n.Remediation.Findings != "" {
			fmt.Printf("Plan:      %s\n", wordWrap(n.Remediation.Findings, 70, "           "))
		}
		if len(n.Remediation.Steps) > 0 {
			stepNames := make([]string, 0, len(n.Remediation.Steps))
			for _, s := range n.Remediation.Steps {
				prefix := "✓"
				if s.Status == "failed" || s.Status == "error" {
					prefix = "✗"
				}
				stepNames = append(stepNames, prefix+" "+s.StepName)
			}
			fmt.Printf("Steps:     %s\n", strings.Join(stepNames, "  "))
		}
	}

	// ── EVALUATION ───────────────────────────────────────────
	if n.Evaluation != nil {
		section("EVALUATION")
		ev := n.Evaluation
		diagLabel := "heuristic"
		if ev.JudgeUsed {
			diagLabel = "LLM judge"
		}
		// Overall score matches the SCORE column in vault incidents <fault-id>.
		if ev.OverallScore > 0 {
			breakdown := fmt.Sprintf("diagnosis %d%%", int(ev.DiagnosisScore*100))
			if ev.RemediationScore > 0 {
				breakdown += fmt.Sprintf(" · remediation %d%%", int(ev.RemediationScore*100))
			}
			fmt.Printf("Score:         %d%%   (%s)\n", int(ev.OverallScore*100), breakdown)
		}
		fmt.Printf("Diagnosis:     %.2f (%s)", ev.DiagnosisScore, diagLabel)
		if ev.PrimaryConfidence > 0 {
			fmt.Printf("   Agent confidence: %.0f%%", ev.PrimaryConfidence*100)
		}
		fmt.Println()
		if ev.RemediationJudgeScore > 0 {
			fmt.Printf("Remediation:   %.2f (LLM judge)\n", ev.RemediationJudgeScore)
		}
		if ev.RemediationJudgeReasoning != "" {
			fmt.Printf("Reasoning:     %s\n", wordWrap(ev.RemediationJudgeReasoning, 70, "               "))
		}
	}

	// ── POST-INCIDENT FEEDBACK ────────────────────────────────
	var postFeedback []narrativeFeedback
	for _, fb := range n.Feedback {
		if fb.FeedbackTime == "post_incident" {
			postFeedback = append(postFeedback, fb)
		}
	}
	if len(postFeedback) > 0 {
		section("POST-INCIDENT FEEDBACK")
		for _, fb := range postFeedback {
			verdict := "–"
			if fb.VerdictCorrect != nil {
				if *fb.VerdictCorrect {
					verdict = "✓ correct"
				} else {
					verdict = "✗ wrong"
				}
			}
			if fb.VerdictNotes != "" {
				verdict += fmt.Sprintf(" (%s)", fb.VerdictNotes)
			}
			fmt.Printf("  %-28s %s\n", fb.FeedbackType+":", verdict)
		}
	}

	// ── JOURNEYS ─────────────────────────────────────────────
	if len(n.Journeys) > 0 {
		section("JOURNEYS")
		fmt.Println("  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)")
		fmt.Println()
		for _, j := range n.Journeys {
			label := j.Phase
			desc := ""
			switch j.Phase {
			case "triage":
				desc = "reasoning chain, hypothesis building"
			case "remediation":
				desc = "tool calls, approvals, blast-radius decisions"
			case "triage+remediation":
				desc = "full session: diagnosis through fix"
			}
			fmt.Printf("  %-22s %s\n", label+":", j.TraceID)
			if desc != "" {
				fmt.Printf("  %-22s %s\n", "", desc)
			}
		}
		fmt.Println()
		fmt.Printf("  → vault journeys %s\n", n.Journeys[0].TraceID)
	}
	fmt.Println()
}

// wordWrap wraps text at maxWidth characters, indenting continuation lines with indent.
// wrapLines splits text into lines of at most maxWidth characters, breaking
// on word boundaries. It handles existing newlines in the source text.
func wrapLines(text string, maxWidth int) []string {
	var result []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		words := strings.Fields(para)
		current := ""
		for _, w := range words {
			if current == "" {
				current = w
			} else if len(current)+1+len(w) <= maxWidth {
				current += " " + w
			} else {
				result = append(result, current)
				current = w
			}
		}
		if current != "" {
			result = append(result, current)
		}
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wordWrap(text string, maxWidth int, indent string) string {
	if len(text) <= maxWidth {
		return text
	}
	words := strings.Fields(text)
	var lines []string
	current := ""
	for _, w := range words {
		if current == "" {
			current = w
		} else if len(current)+1+len(w) <= maxWidth {
			current += " " + w
		} else {
			lines = append(lines, current)
			current = w
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n"+indent)
}

// ── vault active ──────────────────────────────────────────────────────────

// vaultActive lists the currently active version of every playbook series.
func vaultActive(args []string) {
	fs := flag.NewFlagSet("vault active", flag.ExitOnError)
	var gatewayURL, apiKey string
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks?active_only=true&include_system=true"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	var result struct {
		Playbooks []struct {
			PlaybookID string `json:"playbook_id"`
			SeriesID   string `json:"series_id"`
			Version    string `json:"version"`
			Name       string `json:"name"`
			Source     string `json:"source"`
			UpdatedAt  string `json:"updated_at"`
		} `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response: %v\n", err)
		os.Exit(1)
	}

	pbs := result.Playbooks
	if len(pbs) == 0 {
		fmt.Println("No active playbooks.")
		return
	}

	const (
		colSer  = 34
		colVer  = 7
		colSrc  = 9
		colDate = 10
		colName = 44
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n", colSer, "SERIES", colVer, "VERSION", colSrc, "SOURCE", colDate, "UPDATED", "NAME")
	fmt.Println(strings.Repeat("─", colSer+2+colVer+2+colSrc+2+colDate+2+colName))
	for _, pb := range pbs {
		ts := pb.UpdatedAt
		if len(ts) >= 10 {
			ts = ts[:10]
		}
		name := pb.Name
		if len(name) > colName {
			name = name[:colName-1] + "…"
		}
		ser := pb.SeriesID
		if len(ser) > colSer {
			ser = ser[:colSer-1] + "…"
		}
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n", colSer, ser, colVer, pb.Version, colSrc, pb.Source, colDate, ts, name)
	}
}

// ── vault history ─────────────────────────────────────────────────────────

// vaultHistory lists every stored version of a playbook series — active, inactive,
// system, and generated — regardless of whether any runs have been recorded.
// This is the complete provenance ledger for a series.
func vaultHistory(args []string) {
	fs := flag.NewFlagSet("vault history", flag.ExitOnError)
	var gatewayURL, apiKey string
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault history <series-id> [--gateway ...] [--api-key ...]")
		fmt.Fprintln(os.Stderr, "  Lists every version stored for a series, including inactive and system versions.")
		fmt.Fprintln(os.Stderr, "  Use playbook IDs from the ID column with 'vault diff <id1> <id2>'.")
		os.Exit(1)
	}
	seriesID := fs.Arg(0)
	if strings.HasPrefix(seriesID, "pb_") {
		fmt.Fprintf(os.Stderr, "Error: %q looks like a playbook ID, not a series ID.\n", seriesID)
		fmt.Fprintf(os.Stderr, "  Use a series ID like pbs_connection_remediate.\n")
		os.Exit(1)
	}

	url := strings.TrimSuffix(gatewayURL, "/") +
		"/api/v1/fleet/playbooks?series_id=" + seriesID + "&active_only=false&include_system=true"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	var result struct {
		Playbooks []struct {
			PlaybookID  string `json:"playbook_id"`
			Version     string `json:"version"`
			IsActive    bool   `json:"is_active"`
			Source      string `json:"source"`
			OriginTrace string `json:"origin_trace"`
			CreatedAt   string `json:"created_at"`
			Name        string `json:"name"`
		} `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response: %v\n", err)
		os.Exit(1)
	}

	if len(result.Playbooks) == 0 {
		fmt.Printf("No versions found for series %q.\n", seriesID)
		return
	}

	fmt.Printf("Version history for %s — %d version(s)\n\n", seriesID, len(result.Playbooks))

	const (
		colID   = 14
		colVer  = 9
		colSrc  = 9
		colDate = 10
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n", colID, "ID", colVer, "VERSION", colSrc, "SOURCE", colDate, "CREATED", "STATUS / NAME")
	fmt.Println(strings.Repeat("─", colID+2+colVer+2+colSrc+2+colDate+2+50))
	for _, pb := range result.Playbooks {
		ts := pb.CreatedAt
		if len(ts) >= 10 {
			ts = ts[:10]
		}
		status := ""
		if pb.IsActive {
			status = "* "
		}
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s%s\n",
			colID, pb.PlaybookID,
			colVer, pb.Version,
			colSrc, pb.Source,
			colDate, ts,
			status, pb.Name,
		)
		if pb.OriginTrace != "" {
			fmt.Printf("  %*s  from=%s\n", colID, "", pb.OriginTrace)
		}
	}
	fmt.Println()
	fmt.Println("* = currently active version")
	fmt.Println("Use: faulttest vault diff <id1> <id2> to compare any two versions.")
}

// ── vault drafts ──────────────────────────────────────────────────────────

// ── vault activate ────────────────────────────────────────────────────────

// vaultActivate promotes a draft playbook to active status in its series.
func vaultActivate(args []string) {
	fs := flag.NewFlagSet("vault activate", flag.ExitOnError)
	var gatewayURL, apiKey string
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault activate <draft-id> [--gateway ...] [--api-key ...]")
		fmt.Fprintln(os.Stderr, "  Run 'faulttest vault drafts' to list pending draft IDs.")
		os.Exit(1)
	}
	draftID := fs.Arg(0)

	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/" + draftID + "/activate"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	var pb struct {
		PlaybookID string `json:"playbook_id"`
		SeriesID   string `json:"series_id"`
		Version    string `json:"version"`
		Name       string `json:"name"`
		IsActive   bool   `json:"is_active"`
	}
	if err := json.Unmarshal(body, &pb); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Activated: %s  v%s  %s\n", pb.PlaybookID, pb.Version, pb.Name)
	fmt.Printf("Series:    %s\n", pb.SeriesID)
	fmt.Printf("\nfaulttest vault active --gateway %s --api-key <key>\n", strings.TrimSuffix(gatewayURL, "/"))
}

// ── vault diff ────────────────────────────────────────────────────────────

// diffPlaybook holds the fields compared by vault diff.
type diffPlaybook struct {
	PlaybookID    string   `json:"playbook_id"`
	SeriesID      string   `json:"series_id"`
	Version       string   `json:"version"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Guidance      string   `json:"guidance"`
	Symptoms      []string `json:"symptoms"`
	Escalation    []string `json:"escalation"`
	ExecutionMode string   `json:"execution_mode"`
	ApprovalMode  string   `json:"approval_mode"`
	IsActive      bool     `json:"is_active"`
}

func fetchPlaybookByID(gatewayURL, apiKey, playbookID string) (*diffPlaybook, error) {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/" + playbookID
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, body)
	}
	var pb diffPlaybook
	if err := json.Unmarshal(body, &pb); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &pb, nil
}

// vaultDiff compares two playbook versions field by field. Unchanged fields are omitted.
//
// Single-ID mode:  vault diff <draft-id>        — draft vs currently active in its series
// Two-ID mode:     vault diff <id1> <id2>        — compare any two versions by playbook_id
//
// Pass --judge to request an LLM review of the knowledge-field changes.
func vaultDiff(args []string) {
	fs := flag.NewFlagSet("vault diff", flag.ExitOnError)
	var gatewayURL, apiKey string
	var judgeEnabled bool
	var judgeModel, judgeVendor, judgeAPIKey string
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	fs.BoolVar(&judgeEnabled, "judge", false, "Run LLM-as-judge review of the proposed version")
	fs.StringVar(&judgeModel, "judge-model", "", "Model name for judge (default: HELPDESK_MODEL_NAME)")
	fs.StringVar(&judgeVendor, "judge-vendor", "", "Model vendor for judge: anthropic or google (default: HELPDESK_MODEL_VENDOR)")
	fs.StringVar(&judgeAPIKey, "judge-api-key", os.Getenv("HELPDESK_API_KEY"), "API key for judge")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault diff <draft-id> [<second-id>] [--gateway ...] [--api-key ...]")
		fmt.Fprintln(os.Stderr, "  Single ID: compares draft against the currently active version in its series.")
		fmt.Fprintln(os.Stderr, "  Two IDs:   compares any two versions directly (use 'vault versions' to get IDs).")
		fmt.Fprintln(os.Stderr, "  --judge:   ask an LLM to review whether the change is an improvement.")
		os.Exit(1)
	}
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "pbs_") {
			fmt.Fprintf(os.Stderr, "Error: %q looks like a series ID, not a playbook ID.\n", arg)
			fmt.Fprintf(os.Stderr, "  Run: faulttest vault versions %s --gateway ... to list playbook IDs.\n", arg)
			os.Exit(1)
		}
	}

	var before, after *diffPlaybook

	if fs.NArg() >= 2 {
		// Two-ID mode: fetch both by ID, treat the lower version as "before".
		id1, id2 := fs.Arg(0), fs.Arg(1)
		pb1, err := fetchPlaybookByID(gatewayURL, apiKey, id1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id1, err)
			os.Exit(1)
		}
		pb2, err := fetchPlaybookByID(gatewayURL, apiKey, id2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id2, err)
			os.Exit(1)
		}
		// Order by version so "before" is always the older one.
		if compareVersions(pb1.Version, pb2.Version) <= 0 {
			before, after = pb1, pb2
		} else {
			before, after = pb2, pb1
		}
	} else {
		// Single-ID mode: draft vs active.
		draftID := fs.Arg(0)
		draft, err := fetchPlaybookByID(gatewayURL, apiKey, draftID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching draft %s: %v\n", draftID, err)
			os.Exit(1)
		}
		if draft.IsActive {
			fmt.Fprintf(os.Stderr, "%s is already active — use two-ID mode: vault diff <id1> <id2>\n", draftID)
			fmt.Fprintf(os.Stderr, "  Run 'faulttest vault versions <fault-id>' to look up playbook IDs.\n")
			os.Exit(1)
		}
		current, err := fetchActivePlaybook(gatewayURL, apiKey, draft.SeriesID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching active playbook for series %s: %v\n", draft.SeriesID, err)
			os.Exit(1)
		}
		active, err := fetchPlaybookByID(gatewayURL, apiKey, current.PlaybookID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching active playbook %s: %v\n", current.PlaybookID, err)
			os.Exit(1)
		}
		before, after = active, draft
	}

	sep := strings.Repeat("─", 72)
	fmt.Printf("Diff: series %s\n", before.SeriesID)
	fmt.Printf("  before  %s  v%s  %s\n", before.PlaybookID, before.Version, before.Name)
	fmt.Printf("  after   %s  v%s  %s\n\n", after.PlaybookID, after.Version, after.Name)

	changed := 0
	var operationalDrift []string

	diffField := func(label, cur, prop string) {
		if cur == prop {
			return
		}
		changed++
		fmt.Printf("── %s %s\n", label, sep[:max(0, 68-len(label))])
		printDiffBlock("before", cur)
		printDiffBlock("after ", prop)
		fmt.Println()
	}
	diffList := func(label string, cur, prop []string) {
		diffField(label, strings.Join(cur, "\n"), strings.Join(prop, "\n"))
	}
	// trackOperational records a changed operational field so the judge can flag it.
	trackOperational := func(label, cur, prop string) {
		if cur != prop {
			operationalDrift = append(operationalDrift,
				fmt.Sprintf("%s: %s → %s", label, cur, prop))
		}
		diffField(label, cur, prop)
	}

	diffField("name", before.Name, after.Name)
	diffField("description", before.Description, after.Description)
	diffField("guidance", before.Guidance, after.Guidance)
	diffList("symptoms", before.Symptoms, after.Symptoms)
	diffList("escalation", before.Escalation, after.Escalation)
	trackOperational("execution_mode", before.ExecutionMode, after.ExecutionMode)
	trackOperational("approval_mode", before.ApprovalMode, after.ApprovalMode)

	if changed == 0 {
		fmt.Println("No differences — the two versions are identical.")
		return
	}
	fmt.Printf("%d field(s) changed.\n\n", changed)
	if !after.IsActive {
		fmt.Printf("To activate:  faulttest vault activate %s --gateway %s --api-key <key>\n",
			after.PlaybookID, strings.TrimSuffix(gatewayURL, "/"))
		fmt.Printf("To discard:   curl -X DELETE %s/api/v1/fleet/playbooks/%s -H 'Authorization: Bearer <key>'\n",
			strings.TrimSuffix(gatewayURL, "/"), after.PlaybookID)
	}

	if !judgeEnabled {
		return
	}

	// LLM-as-judge review of the proposed version.
	fmt.Printf("\n── LLM Judge Review %s\n", strings.Repeat("─", 52))

	if len(operationalDrift) > 0 {
		fmt.Printf("⚠  Operational field changes detected (should be preserved by from-trace):\n")
		for _, d := range operationalDrift {
			fmt.Printf("   • %s\n", d)
		}
		fmt.Println()
	}

	cfg := &HarnessConfig{
		JudgeVendor: judgeVendor,
		JudgeModel:  judgeModel,
		JudgeAPIKey: judgeAPIKey,
	}
	completer, err := newJudgeCompleter(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Judge error: could not initialize LLM: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Set HELPDESK_MODEL_VENDOR / HELPDESK_MODEL_NAME / HELPDESK_API_KEY or use --judge-* flags.\n")
		os.Exit(1)
	}

	modelName := judgeModel
	if modelName == "" {
		modelName = os.Getenv("HELPDESK_MODEL_NAME")
	}

	result := faultlib.JudgePlaybookDiff(
		context.Background(),
		faultlib.PlaybookDiffInput{
			BeforeID:          before.PlaybookID,
			AfterID:           after.PlaybookID,
			BeforeName:        before.Name,
			BeforeDescription: before.Description,
			BeforeGuidance:    before.Guidance,
			BeforeSymptoms:    before.Symptoms,
			BeforeEscalation:  before.Escalation,
			AfterName:         after.Name,
			AfterDescription:  after.Description,
			AfterGuidance:     after.Guidance,
			AfterSymptoms:     after.Symptoms,
			AfterEscalation:   after.Escalation,
			OperationalDrift:  operationalDrift,
		},
		before.Version,
		after.Version,
		completer,
		modelName,
	)

	if result.Skipped {
		fmt.Printf("Judge skipped: %s\n", result.Reasoning)
		return
	}

	verdictIcon := map[string]string{
		"APPROVE":      "✓",
		"NEEDS_REVIEW": "?",
		"REJECT":       "✗",
	}
	icon := verdictIcon[result.Verdict]
	if icon == "" {
		icon = "?"
	}
	fmt.Printf("Verdict:            %s  %s\n", icon, result.Verdict)
	fmt.Printf("Guidance quality:   %s\n", result.GuidanceQuality)
	fmt.Printf("Escalation safety:  %s\n", result.EscalationSafety)
	fmt.Printf("Reasoning:          %s\n", result.Reasoning)
	if modelName != "" {
		fmt.Printf("Judge model:        %s\n", modelName)
	}

	// Auto-save the verdict to the draft so vault versions can later correlate
	// whether the judge's prediction matched the actual improvement.
	if gatewayURL != "" && !result.Skipped && result.Verdict != "" {
		verdictBody, _ := json.Marshal(map[string]string{
			"verdict":     result.Verdict,
			"judge_model": modelName,
		})
		verdURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/" + after.PlaybookID + "/judge-verdict"
		req, err := http.NewRequest(http.MethodPost, verdURL, strings.NewReader(string(verdictBody)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil || (resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK) {
				fmt.Printf("(Note: verdict recorded locally but could not be saved to gateway: %v)\n", err)
			} else {
				fmt.Printf("Verdict saved to %s.\n", after.PlaybookID)
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
	}
}

// compareVersions compares two dotted-numeric version strings.
// Returns negative if a < b, 0 if equal, positive if a > b.
// Non-numeric components fall back to string comparison.
func compareVersions(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var sa, sb string
		if i < len(pa) {
			sa = pa[i]
		}
		if i < len(pb) {
			sb = pb[i]
		}
		ia, errA := strconv.Atoi(sa)
		ib, errB := strconv.Atoi(sb)
		if errA == nil && errB == nil {
			if ia != ib {
				return ia - ib
			}
		} else {
			if sa != sb {
				if sa < sb {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}

// printDiffBlock prints a labelled multi-line value, indenting each line.
func printDiffBlock(label, value string) {
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	prefix := "  " + label + "  "
	blank := strings.Repeat(" ", len(prefix))
	for i, line := range lines {
		if i == 0 {
			fmt.Printf("%s%s\n", prefix, line)
		} else {
			fmt.Printf("%s%s\n", blank, line)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// purgeOrphanDrafts deletes every draft whose series_id has the pbs_generated_
// prefix (orphans created before series-pinning was in place). Returns the count
// of successfully deleted drafts.
func purgeOrphanDrafts(gatewayURL, apiKey string, drafts []struct {
	PlaybookID string `json:"playbook_id"`
	SeriesID   string `json:"series_id"`
	Version    string `json:"version"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	CreatedAt  string `json:"created_at"`
}) int {
	client := &http.Client{Timeout: 15 * time.Second}
	deleted := 0
	for _, d := range drafts {
		if !strings.HasPrefix(d.SeriesID, "pbs_generated_") {
			continue
		}
		delURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/" + d.PlaybookID
		delReq, _ := http.NewRequest(http.MethodDelete, delURL, nil)
		if apiKey != "" {
			delReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		delResp, delErr := client.Do(delReq)
		if delErr != nil || delResp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "Failed to delete %s: %v\n", d.PlaybookID, delErr)
			continue
		}
		delResp.Body.Close()
		fmt.Printf("Deleted orphan draft %s (%s)\n", d.PlaybookID, d.Name)
		deleted++
	}
	return deleted
}

// vaultDrafts lists inactive generated playbook drafts waiting for review and activation.
func vaultDrafts(args []string) {
	fs := flag.NewFlagSet("vault drafts", flag.ExitOnError)
	var gatewayURL, apiKey string
	var purgeOrphans bool
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	fs.BoolVar(&purgeOrphans, "purge-orphans", false, "Delete all drafts whose series_id starts with pbs_generated_ (orphans from failed suggest-update runs)")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks?active_only=false"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	var result struct {
		Playbooks []struct {
			PlaybookID string `json:"playbook_id"`
			SeriesID   string `json:"series_id"`
			Version    string `json:"version"`
			Name       string `json:"name"`
			Source     string `json:"source"`
			CreatedAt  string `json:"created_at"`
			IsActive   bool   `json:"is_active"`
		} `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response: %v\n", err)
		os.Exit(1)
	}

	// Filter to inactive drafts with a tracked source (generated or imported).
	// Active playbooks have already been reviewed; empty-source records are
	// internal operational playbooks not intended for this queue.
	var drafts []struct {
		PlaybookID string `json:"playbook_id"`
		SeriesID   string `json:"series_id"`
		Version    string `json:"version"`
		Name       string `json:"name"`
		Source     string `json:"source"`
		CreatedAt  string `json:"created_at"`
	}
	for _, p := range result.Playbooks {
		if !p.IsActive && (p.Source == "generated" || p.Source == "imported") {
			drafts = append(drafts, struct {
				PlaybookID string `json:"playbook_id"`
				SeriesID   string `json:"series_id"`
				Version    string `json:"version"`
				Name       string `json:"name"`
				Source     string `json:"source"`
				CreatedAt  string `json:"created_at"`
			}{p.PlaybookID, p.SeriesID, p.Version, p.Name, p.Source, p.CreatedAt})
		}
	}

	if purgeOrphans {
		deleted := purgeOrphanDrafts(gatewayURL, apiKey, drafts)
		fmt.Printf("\nPurged %d orphan draft(s).\n", deleted)
		return
	}

	if len(drafts) == 0 {
		fmt.Println("No pending drafts.")
		return
	}

	fmt.Printf("Pending drafts — %d awaiting review\n\n", len(drafts))
	const (
		colID   = 12
		colSer  = 30
		colVer  = 7
		colName = 42
		colDate = 10
	)
	orphans := 0
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n", colID, "DRAFT ID", colSer, "SERIES", colVer, "VERSION", colName, "NAME", "CREATED")
	fmt.Println(strings.Repeat("─", colID+2+colSer+2+colVer+2+colName+2+colDate))
	imported := 0
	for _, d := range drafts {
		ts := d.CreatedAt
		if len(ts) >= 10 {
			ts = ts[:10]
		}
		name := d.Name
		if len(name) > colName {
			name = name[:colName-1] + "…"
		}
		ser := d.SeriesID
		tag := ""
		if strings.HasPrefix(ser, "pbs_generated_") {
			tag = " !"
			orphans++
		} else if d.Source == "imported" {
			tag = " ↑"
			imported++
		}
		if len(ser) > colSer {
			ser = ser[:colSer-1] + "…"
		}
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s%s\n", colID, d.PlaybookID, colSer, ser, colVer, d.Version, colName, name, ts, tag)
	}
	if orphans > 0 {
		fmt.Printf("\n! = orphan draft (series not pinned); run with --purge-orphans to delete all %d\n", orphans)
	}
	if imported > 0 {
		fmt.Printf("↑ = imported via vault import\n")
	}
	fmt.Printf("\nTo activate a draft:\n")
	fmt.Printf("  faulttest vault activate <DRAFT_ID> --gateway %s --api-key <key>\n", strings.TrimSuffix(gatewayURL, "/"))
}

// ── vault discard ─────────────────────────────────────────────────────────

func vaultDiscard(args []string) {
	fs := flag.NewFlagSet("vault discard", flag.ExitOnError)
	var gatewayURL, apiKey string
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault discard <draft-id> [--gateway ...] [--api-key ...]")
		fmt.Fprintln(os.Stderr, "  Run 'faulttest vault drafts' to list pending draft IDs.")
		os.Exit(1)
	}
	draftID := fs.Arg(0)

	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/" + draftID
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		fmt.Fprintf(os.Stderr, "Gateway returned %d: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	fmt.Printf("Discarded: %s\n", draftID)
	fmt.Printf("\nfaulttest vault drafts --gateway %s\n", strings.TrimSuffix(gatewayURL, "/"))
}

// ── vault import ──────────────────────────────────────────────────────────

// vaultImport reads a local YAML playbook file, imports it via the gateway's
// /import endpoint (validate only), saves it as a draft, and optionally activates it.
func vaultImport(args []string) {
	fs := flag.NewFlagSet("vault import", flag.ExitOnError)
	var gatewayURL, apiKey string
	var activate, force bool
	var seriesHint string
	fs.StringVar(&gatewayURL, "gateway", os.Getenv("FAULTTEST_GATEWAY_URL"), "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	fs.BoolVar(&activate, "activate", false, "Immediately activate the imported draft")
	fs.BoolVar(&force, "force", false, "Save draft even when the import response contains warnings")
	fs.StringVar(&seriesHint, "series-id", "", "Override the series_id in the YAML (useful for re-importing an edited playbook into an existing series)")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault import <file.yaml> [--activate] [--force] [--gateway ...] [--api-key ...]")
		fmt.Fprintln(os.Stderr, "  Reads a YAML playbook file, validates it, saves it as a draft, and optionally activates it.")
		fmt.Fprintln(os.Stderr, "  --activate     Immediately activate the imported draft (skips manual vault activate step)")
		fmt.Fprintln(os.Stderr, "  --force        Save draft even when warnings are present")
		fmt.Fprintln(os.Stderr, "  --series-id    Override the series_id declared in the YAML")
		os.Exit(1)
	}
	if gatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required (or set FAULTTEST_GATEWAY_URL)")
		os.Exit(1)
	}

	filePath := fs.Arg(0)
	yamlBytes, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", filePath, err)
		os.Exit(1)
	}

	// Step 1: POST to /import (validates and parses; does NOT persist).
	hints := map[string]string{}
	if seriesHint != "" {
		hints["series_id"] = seriesHint
	}
	importBody, err := json.Marshal(map[string]any{
		"text":   string(yamlBytes),
		"format": "yaml",
		"hints":  hints,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshalling import request: %v\n", err)
		os.Exit(1)
	}

	importURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/import"
	importReq, err := http.NewRequest(http.MethodPost, importURL, strings.NewReader(string(importBody)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building request: %v\n", err)
		os.Exit(1)
	}
	importReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		importReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	importResp, err := (&http.Client{Timeout: 30 * time.Second}).Do(importReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error calling import endpoint: %v\n", err)
		os.Exit(1)
	}
	defer importResp.Body.Close()
	importRespBody, _ := io.ReadAll(importResp.Body)
	if importResp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Import failed (%d): %s\n", importResp.StatusCode, importRespBody)
		os.Exit(1)
	}

	var importResult struct {
		Draft struct {
			PlaybookID    string `json:"playbook_id"`
			SeriesID      string `json:"series_id"`
			Name          string `json:"name"`
			Version       string `json:"version"`
			ApprovalMode  string `json:"approval_mode"`
			ExecutionMode string `json:"execution_mode"`
			Guidance      string `json:"guidance"`
			Description   string `json:"description"`
			Source        string `json:"source"`
		} `json:"draft"`
		WarningMessages []string `json:"warning_messages"`
		Confidence      float64  `json:"confidence"`
	}
	if err := json.Unmarshal(importRespBody, &importResult); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding import response: %v\n", err)
		os.Exit(1)
	}

	// Print warnings.
	if len(importResult.WarningMessages) > 0 {
		fmt.Printf("Import warnings (%d):\n", len(importResult.WarningMessages))
		for _, w := range importResult.WarningMessages {
			fmt.Printf("  ⚠ %s\n", w)
		}
		if !force {
			fmt.Fprintln(os.Stderr, "\nFix warnings or use --force to save anyway.")
			os.Exit(1)
		}
		fmt.Println()
	}

	d := importResult.Draft
	fmt.Printf("Parsed: %s  v%s  (%s)\n", d.Name, d.Version, d.SeriesID)
	if importResult.Confidence < 1.0 {
		fmt.Printf("Confidence: %.0f%%\n", importResult.Confidence*100)
	}

	// Step 2: Save the draft (persist via POST /api/v1/fleet/playbooks).
	// Tag the draft as imported so vault drafts can surface it in the review queue.
	importResult.Draft.Source = "imported"
	saveBody, err := json.Marshal(importResult.Draft)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshalling draft: %v\n", err)
		os.Exit(1)
	}
	saveURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks"
	saveReq, err := http.NewRequest(http.MethodPost, saveURL, strings.NewReader(string(saveBody)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building save request: %v\n", err)
		os.Exit(1)
	}
	saveReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		saveReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	saveResp, err := (&http.Client{Timeout: 15 * time.Second}).Do(saveReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving draft: %v\n", err)
		os.Exit(1)
	}
	defer saveResp.Body.Close()
	saveRespBody, _ := io.ReadAll(saveResp.Body)
	if saveResp.StatusCode != http.StatusCreated && saveResp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Save failed (%d): %s\n", saveResp.StatusCode, saveRespBody)
		os.Exit(1)
	}

	var saved struct {
		PlaybookID string `json:"playbook_id"`
		SeriesID   string `json:"series_id"`
		Version    string `json:"version"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(saveRespBody, &saved); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding save response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Saved:  %s  v%s  series=%s\n", saved.PlaybookID, saved.Version, saved.SeriesID)

	// Step 3: Optionally activate.
	if activate {
		activateURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/fleet/playbooks/" + saved.PlaybookID + "/activate"
		actReq, err := http.NewRequest(http.MethodPost, activateURL, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building activate request: %v\n", err)
			os.Exit(1)
		}
		if apiKey != "" {
			actReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		actResp, err := (&http.Client{Timeout: 15 * time.Second}).Do(actReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error activating: %v\n", err)
			os.Exit(1)
		}
		defer actResp.Body.Close()
		actBody, _ := io.ReadAll(actResp.Body)
		if actResp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "Activate failed (%d): %s\n", actResp.StatusCode, actBody)
			os.Exit(1)
		}
		fmt.Printf("Activated: %s is now live.\n", saved.PlaybookID)
	} else {
		fmt.Printf("\nTo diff before activating:\n  faulttest vault diff %s --gateway %s\n", saved.PlaybookID, strings.TrimSuffix(gatewayURL, "/"))
		fmt.Printf("To activate:\n  faulttest vault activate %s --gateway %s\n", saved.PlaybookID, strings.TrimSuffix(gatewayURL, "/"))
	}
}

// ── vault calibration ─────────────────────────────────────────────────────

// vaultCalibration shows confidence-band calibration: how well diagnosis_score
// predicts operator-confirmed correctness.
// Usage: faulttest vault calibration [<fault-id or series-id>] [--gateway ...] [--api-key ...]
func vaultCalibration(args []string) {
	fs := flag.NewFlagSet("vault calibration", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault calibration")
		os.Exit(1)
	}

	seriesID := ""
	if len(fs.Args()) > 0 {
		arg := fs.Args()[0]
		seriesID = arg
		// Resolve fault ID → series ID.
		if !strings.HasPrefix(arg, "pbs_") {
			cat, err := loadActiveCatalog(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading catalog: %v\n", err)
				os.Exit(1)
			}
			var found bool
			for _, f := range cat.Failures {
				if f.ID == arg {
					if f.DiagnosisPlaybookSeriesID == "" {
						fmt.Fprintf(os.Stderr, "Fault %q has no diagnosis playbook.\n", arg)
						os.Exit(1)
					}
					seriesID = f.DiagnosisPlaybookSeriesID
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "Unknown fault ID %q. Run `faulttest list` to see available faults.\n", arg)
				os.Exit(1)
			}
		}
	}

	report, err := fetchCalibration(cfg.GatewayURL, cfg.GatewayAPIKey, seriesID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching calibration: %v\n", err)
		os.Exit(1)
	}

	scope := "fleet-wide"
	if report.SeriesID != "" {
		scope = report.SeriesID
	}

	// Prominent data quality banner before the table when human coverage is low.
	if report.TotalRuns > 0 && (report.HumanRuns == 0 || float64(report.HumanRuns)/float64(report.TotalRuns) < 0.5) {
		bl := func(s string) { fmt.Printf("│%-69s│\n", s) }
		fmt.Println("┌─────────────────────────────────────────────────────────────────────┐")
		if report.HumanRuns == 0 {
			bl(fmt.Sprintf(" ⚠  Data quality: 0 of %d run(s) have human operator feedback.", report.TotalRuns))
		} else {
			bl(fmt.Sprintf(" ⚠  Data quality: only %d of %d run(s) have human operator feedback.", report.HumanRuns, report.TotalRuns))
		}
		bl("    This table measures self-consistency (LLM judge vs. itself),")
		bl("    not calibration against human judgment. To build a meaningful")
		bl("    calibration dataset, run faulttest interactively (without")
		bl("    --approval-mode force) or submit feedback via:")
		bl("      faulttest vault feedback <run-id> --gateway $GW --api-key $KEY")
		fmt.Println("└─────────────────────────────────────────────────────────────────────┘")
		fmt.Println()
	}

	fmt.Printf("Diagnosis calibration — %s (%d runs with agent confidence + operator feedback)\n", scope, report.TotalRuns)
	if report.HeuristicCount > 0 {
		fmt.Printf("(%d run(s) excluded — agent did not emit a CONFIDENCE: value on primary hypothesis)\n", report.HeuristicCount)
	}
	fmt.Println()

	const (
		colBand  = 12
		colRuns  = 6
		colCorr  = 9
		colAccu  = 10
		colCalib = 20
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n",
		colBand, "CONFIDENCE", colRuns, "RUNS", colCorr, "CORRECT", colAccu, "ACCURACY", "CALIBRATION")
	fmt.Println(strings.Repeat("─", colBand+2+colRuns+2+colCorr+2+colAccu+2+colCalib))

	printCalibBands := func(bands []calibrationBand) {
		for _, b := range bands {
			accuStr := "–"
			if b.Runs > 0 {
				accuStr = fmt.Sprintf("%d%%", int(b.ActualAccuracy*100))
			}
			note := ""
			if b.HeuristicRuns > 0 {
				note = fmt.Sprintf("  ⚠ %d/%d keyword (no judge)", b.HeuristicRuns, b.Runs)
			}
			fmt.Printf("%-*s  %-*d  %-*d  %-*s  %s%s\n",
				colBand, b.Band,
				colRuns, b.Runs,
				colCorr, b.Correct,
				colAccu, accuStr,
				b.Calibration,
				note,
			)
		}
	}

	printCalibBands(report.Bands)

	if report.TotalRuns == 0 {
		fmt.Println()
		fmt.Println("No runs with both eval scores and operator feedback yet.")
		fmt.Println("Run faulttest with --gateway and submit feedback via `vault incidents` to populate.")
	} else if report.AutoJudgeRuns > 0 {
		fmt.Println()
		if report.HumanRuns == 0 {
			fmt.Printf("Note: all %d run(s) above use auto_judge feedback (LLM judge score ≥ 0.8, --approval-mode force).\n", report.AutoJudgeRuns)
			fmt.Println("      Calibration measures self-consistency, not human judgment. Run interactively to collect real operator verdicts.")
		} else {
			fmt.Printf("Sources: %d human operator verdict(s), %d auto_judge (LLM judge score, --approval-mode force)\n", report.HumanRuns, report.AutoJudgeRuns)
		}
	}

	if report.RemediationRuns > 0 {
		fmt.Printf("\nRemediation calibration — %s (%d runs with remediation score + operator feedback)\n\n", scope, report.RemediationRuns)
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n",
			colBand, "SCORE BAND", colRuns, "RUNS", colCorr, "CORRECT", colAccu, "ACCURACY", "CALIBRATION")
		fmt.Println(strings.Repeat("─", colBand+2+colRuns+2+colCorr+2+colAccu+2+colCalib))
		printCalibBands(report.RemediationBands)
	}
}

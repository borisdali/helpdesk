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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault <list|status|drift|accuracy|incidents|versions|calibration|suggest|suggest-update>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  list            Show fault↔playbook pairings and last-run status")
		fmt.Fprintln(os.Stderr, "  status          Show pass rate trends from run history")
		fmt.Fprintln(os.Stderr, "  drift           Highlight faults/playbooks with declining pass rates")
		fmt.Fprintln(os.Stderr, "  accuracy        Show diagnosis accuracy for a playbook series")
		fmt.Fprintln(os.Stderr, "  incidents       List incident run IDs for a fault with feedback status")
		fmt.Fprintln(os.Stderr, "  versions        Show per-version run stats for a playbook series")
		fmt.Fprintln(os.Stderr, "  calibration     Show how well diagnosis scores predict operator-confirmed accuracy")
		fmt.Fprintln(os.Stderr, "  suggest         Generate a playbook draft from an audit trace")
		fmt.Fprintln(os.Stderr, "  suggest-update  Show proposed update for an existing playbook from a trace")
		os.Exit(1)
	}
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
	case "versions":
		vaultVersions(args[1:])
	case "calibration":
		vaultCalibration(args[1:])
	case "suggest":
		vaultSuggest(args[1:])
	case "suggest-update":
		vaultSuggestUpdate(args[1:])
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
	}
	return info
}

// stabilityInfo is a lightweight view of one fault's stability cert for vault list.
type stabilityInfo struct {
	IsStable    bool
	NRuns       int
	TestedAt    time.Time
	hasData     bool
}

// fetchStabilityCert fetches the stability cert for a single fault from the gateway.
// Returns nil when not found or on error.
func fetchStabilityCert(gatewayURL, apiKey, faultID string) *struct {
	FaultID          string  `json:"fault_id"`
	FaultName        string  `json:"fault_name"`
	PlaybookSeriesID string  `json:"playbook_series_id"`
	DiagnosisModel   string  `json:"diagnosis_model"`
	JudgeModel       string  `json:"judge_model"`
	NRuns            int     `json:"n_runs"`
	PassRate         float64 `json:"pass_rate"`
	ConfRangePP      int     `json:"conf_range_pp"`
	IsStable         bool    `json:"is_stable"`
	TestedAt         string  `json:"tested_at"`
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
		FaultID          string  `json:"fault_id"`
		FaultName        string  `json:"fault_name"`
		PlaybookSeriesID string  `json:"playbook_series_id"`
		DiagnosisModel   string  `json:"diagnosis_model"`
		JudgeModel       string  `json:"judge_model"`
		NRuns            int     `json:"n_runs"`
		PassRate         float64 `json:"pass_rate"`
		ConfRangePP      int     `json:"conf_range_pp"`
		IsStable         bool    `json:"is_stable"`
		TestedAt         string  `json:"tested_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cert); err != nil {
		return nil
	}
	return &cert
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
			FaultID  string  `json:"fault_id"`
			NRuns    int     `json:"n_runs"`
			IsStable bool    `json:"is_stable"`
			TestedAt string  `json:"tested_at"`
		} `json:"certs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return out
	}
	for _, c := range result.Certs {
		info := stabilityInfo{IsStable: c.IsStable, NRuns: c.NRuns, hasData: true}
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
	fs.StringVar(&target, "target", "", "Filter last-run history by target (agent-conn alias or hostname)")
	cfg := loadConfig(fs, args)

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
		colDiag      = 26
		colRemed     = 26
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
					lastDate := "-"
					if info.lastRunAt != "" {
						if t, err := time.Parse(time.RFC3339, info.lastRunAt); err == nil {
							lastDate = t.Format("2006-01-02")
						}
					}
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
					incidentCol = fmt.Sprintf("%d runs  %.0f%% resolved  %s  last: %s%s",
						info.totalRuns, info.resolutionRate*100, accuracyStr, lastDate, sourceTag)
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
			// Append age in days when older than 14 days.
			if !si.TestedAt.IsZero() {
				age := int(time.Since(si.TestedAt).Hours() / 24)
				if age >= 14 {
					stableCol += fmt.Sprintf(" %dd", age)
				}
			}
		}

		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %-*s %s\n", colFault, f.ID, colPlatform, platform, colDiag, diagDisplay, colRemed, remedDisplay, colFaultTest, faultTestCol, colStable, stableCol, incidentCol)
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
	if err := fs.Parse(args); err != nil {
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
			printFaultStabilityCert(cfg.GatewayURL, cfg.GatewayAPIKey, faultID)
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
		printFaultStabilityCert(cfg.GatewayURL, cfg.GatewayAPIKey, faultID)
	}
}

// printFaultStabilityCert fetches and prints the stability cert for faultID.
// Called from both the zero-feedback and post-accuracy paths of vaultAccuracy.
func printFaultStabilityCert(gatewayURL, apiKey, faultID string) {
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
				fmt.Println("  [WARN] cert is older than 30 days — consider re-running --repeat to refresh")
			}
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
}

// incidentFeedback is the feedback response shape from GET .../feedback.
type incidentFeedback struct {
	RunID          string `json:"run_id"`
	VerdictCorrect *bool  `json:"verdict_correct"`
	VerdictNotes   string `json:"verdict_notes"`
	Operator       string `json:"operator"`
}

// fetchRunsBySeries calls GET /api/v1/fleet/playbook-runs?series_id=<sid>&limit=<n>.
func fetchRunsBySeries(gatewayURL, apiKey, seriesID string, limit int) ([]incidentRun, error) {
	url := strings.TrimSuffix(gatewayURL, "/") +
		fmt.Sprintf("/api/v1/fleet/playbook-runs?series_id=%s&limit=%d", seriesID, limit)
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
// Usage: faulttest vault incidents <fault-id or series-id> [--limit N]
func vaultIncidents(args []string) {
	fs := flag.NewFlagSet("vault incidents", flag.ExitOnError)
	var limit int
	fs.IntVar(&limit, "limit", 20, "Maximum number of incidents to show")
	cfg := loadConfig(fs, args)

	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault incidents")
		os.Exit(1)
	}
	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault incidents <fault-id or series-id or run-id> [--limit N]")
		os.Exit(1)
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

	// Resolve fault ID → diagnosis series ID via catalog.
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
		colDiag     = 10
		colRemed    = 16
		colFeedback = 12
		colScore    = 5
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colRunID, "RUN ID", colDate, "STARTED", colDiag, "DIAG", colRemed, "REMEDIATION",
		colFeedback, "FEEDBACK", colScore, "SCORE", "FINDINGS")
	fmt.Println(strings.Repeat("─", colRunID+2+colDate+2+colDiag+2+colRemed+2+colFeedback+2+colScore+2+40))

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

		findings := run.FindingsSummary
		if len(findings) > 40 {
			findings = findings[:37] + "..."
		}
		if findings == "" {
			findings = "–"
		}

		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			colRunID, run.RunID,
			colDate, date,
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

// ── vault suggest ─────────────────────────────────────────────────────────

// ── vault suggest-update ──────────────────────────────────────────────────

// vaultPlaybook is a minimal representation of a gateway playbook for suggest-update.
type vaultPlaybook struct {
	PlaybookID  string `json:"playbook_id"`
	Name        string `json:"name"`
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
	fs.StringVar(&traceID, "trace-id", "", "Audit trace ID of the successful incident (required)")
	fs.StringVar(&outcome, "outcome", "resolved", "Incident outcome: resolved or escalated")
	fs.StringVar(&gatewayURL, "gateway", "http://localhost:8080", "Gateway base URL")
	fs.StringVar(&apiKey, "api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "Gateway API key")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if seriesID == "" || traceID == "" {
		fmt.Fprintln(os.Stderr, "Error: --series-id and --trace-id are both required")
		os.Exit(1)
	}

	// Step 1: Fetch current active playbook.
	current, err := fetchActivePlaybook(gatewayURL, apiKey, seriesID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching current playbook: %v\n", err)
		os.Exit(1)
	}

	// Step 2: Synthesize proposed update via from-trace.
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
		Draft      string `json:"draft"`
		Source     string `json:"source"`
		PlaybookID string `json:"playbook_id"`
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
	if err := fs.Parse(args); err != nil {
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
func postStabilityCert(ctx context.Context, cfg *HarnessConfig, f Failure, sr StabilityReport) {
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
	return body.Versions, nil
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

// vaultVersions shows per-version run stats for a playbook series.
// Usage: faulttest vault versions <fault-id or series-id> [--gateway ...] [--api-key ...]
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
	seriesID := arg

	// Resolve fault ID → diagnosis series ID via catalog.
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

	versions, err := fetchVersionStats(cfg.GatewayURL, cfg.GatewayAPIKey, seriesID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching version stats: %v\n", err)
		os.Exit(1)
	}
	if len(versions) == 0 {
		fmt.Printf("No run history found for series %q.\n", seriesID)
		return
	}

	fmt.Printf("Version stats for %s — %d version(s)\n\n", seriesID, len(versions))

	const (
		colVer   = 10
		colRuns  = 6
		colRes   = 10
		colSteps = 10
		colTime  = 10
		colDiag  = 9
		colRemed = 9
	)
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colVer, "VERSION", colRuns, "RUNS", colRes, "TRANSITIONED",
		colSteps, "AVG STEPS", colTime, "AVG TIME", colDiag, "AVG DIAG", "AVG REMED")
	fmt.Println(strings.Repeat("─", colVer+2+colRuns+2+colRes+2+colSteps+2+colTime+2+colDiag+2+colRemed))

	for _, v := range versions {
		ver := v.Version
		if v.IsActive {
			ver += " *"
		}

		resolvedStr := "–"
		if v.TotalRuns > 0 {
			resolvedStr = fmt.Sprintf("%d%%", int(v.TransitionRate*100))
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

		fmt.Printf("%-*s  %-*d  %-*s  %-*s  %-*s  %-*s  %s\n",
			colVer, ver,
			colRuns, v.TotalRuns,
			colRes, resolvedStr,
			colSteps, stepsStr,
			colTime, timeStr,
			colDiag, diagStr,
			remedStr,
		)
	}
	fmt.Println()
	fmt.Println("* = currently active version")
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

// incidentNarrative mirrors gateway.IncidentNarrative for JSON decoding.
type incidentNarrative struct {
	IncidentID  string    `json:"incident_id"`
	StartedAt   time.Time `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	DurationSec float64   `json:"duration_sec,omitempty"`
	Operator    string    `json:"operator"`
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
	Feedback   []narrativeFeedback `json:"feedback,omitempty"`
	Evaluation *narrativeEval      `json:"evaluation,omitempty"`
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
	fmt.Println()
}

// wordWrap wraps text at maxWidth characters, indenting continuation lines with indent.
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

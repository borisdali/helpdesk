package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault <list|status|drift|accuracy|suggest|suggest-update>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  list            Show fault↔playbook pairings and last-run status")
		fmt.Fprintln(os.Stderr, "  status          Show pass rate trends from run history")
		fmt.Fprintln(os.Stderr, "  drift           Highlight faults/playbooks with declining pass rates")
		fmt.Fprintln(os.Stderr, "  accuracy        Show diagnosis accuracy for a playbook series")
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
	}
	return info
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

	runs, _ := loadHistory()

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

	const (
		colFault     = 32
		colPlatform  = 10
		colDiag      = 26
		colRemed     = 26
		colFaultTest = 22 // "2026-04-18  PASS" or "(never)" or "READY"
		// incidents column is the remainder
	)
	fmt.Printf("%-*s %-*s %-*s %-*s %-*s %s\n", colFault, "FAULT", colPlatform, "PLATFORM", colDiag, "DIAG PLAYBOOK", colRemed, "REMED PLAYBOOK", colFaultTest, "FAULT TEST", "INCIDENTS")
	fmt.Println(strings.Repeat("-", colFault+1+colPlatform+1+colDiag+1+colRemed+1+colFaultTest+1+50))

	for _, f := range cat.Failures {
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

		fmt.Printf("%-*s %-*s %-*s %-*s %-*s %s\n", colFault, f.ID, colPlatform, platform, colDiag, diagDisplay, colRemed, remedDisplay, colFaultTest, faultTestCol, incidentCol)
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

func vaultDrift(args []string) {
	fs := flag.NewFlagSet("vault drift", flag.ExitOnError)
	var sinceDays int
	var target string
	fs.IntVar(&sinceDays, "since-days", 90, "Days of history to analyze")
	fs.StringVar(&target, "target", "", "Filter by target (agent-conn alias or hostname)")
	cfg := loadConfig(fs, args)

	runs, err := loadHistory()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(runs) == 0 {
		fmt.Println("No history found.")
		return
	}

	cutoff := time.Now().AddDate(0, 0, -sinceDays)
	mid := cutoff.Add(time.Duration(sinceDays) * 24 * time.Hour / 2)

	type faultStats struct {
		firstHalf  []bool
		secondHalf []bool
	}
	stats := make(map[string]*faultStats)

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
	var drifted []driftEntry
	for id, s := range stats {
		if len(s.firstHalf) == 0 || len(s.secondHalf) == 0 {
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
		return
	}
	fmt.Printf("  Feedback submitted : %d runs\n", info.feedbackCount)
	fmt.Printf("  Correct diagnoses  : %d\n", info.correctCount)
	fmt.Printf("  Accuracy rate      : %.0f%%\n", info.accuracyRate*100)
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
		faultID  string
		seriesID string
		count    int
		correct  int
		rate     float64
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
			e.count = info.feedbackCount
			e.correct = info.correctCount
			e.rate = info.accuracyRate
		}
		if e.count > 0 {
			withFeedback = append(withFeedback, e)
		} else {
			withoutFeedback = append(withoutFeedback, e)
		}
	}

	if len(withFeedback) == 0 && len(withoutFeedback) == 0 {
		fmt.Println("No faults with diagnosis playbooks found in catalog.")
		return
	}

	if len(withFeedback) > 0 {
		colFault := 36
		colSeries := 36
		fmt.Printf("  %-*s %-*s %8s %8s %s\n", colFault, "FAULT", colSeries, "SERIES", "FEEDBACK", "CORRECT", "ACCURACY")
		fmt.Printf("  %-*s %-*s %8s %8s %s\n", colFault, strings.Repeat("─", colFault), colSeries, strings.Repeat("─", colSeries), "────────", "───────", "────────")
		for _, e := range withFeedback {
			fmt.Printf("  %-*s %-*s %8d %8d   %.0f%%\n", colFault, e.faultID, colSeries, e.seriesID, e.count, e.correct, e.rate*100)
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

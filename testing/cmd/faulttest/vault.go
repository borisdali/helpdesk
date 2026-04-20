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
	Target    string               `json:"target,omitempty"`
	Total     int                  `json:"total"`
	Passed    int                  `json:"passed"`
	Results   []historyFaultResult `json:"results"`
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
	runs = append(runs, historyRun{
		RunID:     report.ID,
		Timestamp: report.Timestamp,
		Target:    target,
		Total:     report.Summary.Total,
		Passed:    report.Summary.Passed,
		Results:   faultResults,
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
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault <list|status|drift|suggest|suggest-update>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  list            Show fault↔playbook pairings and last-run status")
		fmt.Fprintln(os.Stderr, "  status          Show pass rate trends from run history")
		fmt.Fprintln(os.Stderr, "  drift           Highlight faults/playbooks with declining pass rates")
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
		colPlaybook  = 26
		colFaultTest = 22 // "2026-04-18  PASS" or "(never)" or "READY"
		// incidents column is the remainder
	)
	fmt.Printf("%-*s %-*s %-*s %s\n", colFault, "FAULT", colPlaybook, "PLAYBOOK", colFaultTest, "FAULT TEST", "INCIDENTS")
	fmt.Println(strings.Repeat("-", colFault+1+colPlaybook+1+colFaultTest+1+50))

	for _, f := range cat.Failures {
		playbookID := f.Remediation.PlaybookID
		playbookDisplay := playbookID
		if playbookDisplay == "" {
			playbookDisplay = "(none)"
		}

		// ── fault test column ────────────────────────────────────────────
		last := lastRun[f.ID]
		var faultTestCol string
		switch {
		case playbookID == "":
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
		incidentCol := "-"
		if playbookID != "" && cfg.GatewayURL != "" {
			info := fetchPlaybookInfo(cfg.GatewayURL, cfg.GatewayAPIKey, playbookID)
			switch info.check {
			case playbookAuthError:
				incidentCol = "AUTH ERROR"
				faultTestCol = faultTestCol // no change
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
					incidentCol = fmt.Sprintf("%d runs  %.0f%% resolved  last: %s%s",
						info.totalRuns, info.resolutionRate*100, lastDate, sourceTag)
				}
			}
		}

		fmt.Printf("%-*s %-*s %-*s %s\n", colFault, f.ID, colPlaybook, playbookDisplay, colFaultTest, faultTestCol, incidentCol)
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
		fmt.Printf("%-10s %-20s %-20s %s\n", "DATE", "TARGET", "RUN ID", "PASS RATE")
		fmt.Println(strings.Repeat("-", 70))
	} else {
		fmt.Printf("%-10s %-20s %s\n", "DATE", "RUN ID", "PASS RATE")
		fmt.Println(strings.Repeat("-", 50))
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
		if target == "" {
			fmt.Printf("%-10s %-20s %-20s %.0f%% (%d/%d)\n", date, run.Target, run.RunID, rate, run.Passed, run.Total)
		} else {
			fmt.Printf("%-10s %-20s %.0f%% (%d/%d)\n", date, run.RunID, rate, run.Passed, run.Total)
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
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

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

	fmt.Printf("%-32s %-12s %-12s %s\n", "FAULT", "FIRST HALF", "SECOND HALF", "DRIFT")
	fmt.Println(strings.Repeat("-", 72))
	for _, d := range drifted {
		fmt.Printf("%-32s %-12s %-12s -%.0f%%\n", d.id,
			fmt.Sprintf("%.0f%%", d.firstRate*100),
			fmt.Sprintf("%.0f%%", d.secondRate*100),
			d.drop*100,
		)
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

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Report is the full structured test report.
type Report struct {
	ID        string       `json:"id"`
	Timestamp string       `json:"timestamp"`
	Results   []EvalResult `json:"results"`
	Summary   Summary      `json:"summary"`
}

// Summary contains aggregate statistics.
type Summary struct {
	Total      int                `json:"total"`
	Passed     int                `json:"passed"`
	Failed     int                `json:"failed"`
	PassRate   float64            `json:"pass_rate"`
	Categories map[string]CatStat `json:"categories"`
}

// CatStat holds per-category pass/fail counts.
type CatStat struct {
	Total  int     `json:"total"`
	Passed int     `json:"passed"`
	Rate   float64 `json:"rate"`
}

// BuildReport creates a Report from evaluation results.
func BuildReport(runID string, results []EvalResult) Report {
	report := Report{
		ID:        runID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Results:   results,
	}

	cats := make(map[string]*CatStat)
	for _, r := range results {
		if _, ok := cats[r.Category]; !ok {
			cats[r.Category] = &CatStat{}
		}
		cats[r.Category].Total++
		if r.Passed {
			cats[r.Category].Passed++
			report.Summary.Passed++
		} else {
			report.Summary.Failed++
		}
	}

	report.Summary.Total = len(results)
	if report.Summary.Total > 0 {
		report.Summary.PassRate = float64(report.Summary.Passed) / float64(report.Summary.Total)
	}

	report.Summary.Categories = make(map[string]CatStat, len(cats))
	for name, stat := range cats {
		if stat.Total > 0 {
			stat.Rate = float64(stat.Passed) / float64(stat.Total)
		}
		report.Summary.Categories[name] = *stat
	}

	return report
}

// WriteJSON writes the report to a JSON file.
func (r Report) WriteJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling report: %v", err)
	}
	return os.WriteFile(path, data, 0644)
}

// PrintSummary writes a human-readable summary to stdout.
func (r Report) PrintSummary() {
	fmt.Printf("\n=== Fault Test Report: %s ===\n\n", r.ID)

	for _, res := range r.Results {
		status := "PASS"
		if !res.Passed {
			status = "FAIL"
		}
		scorePercent := int(res.Score * 100)

		fmt.Printf("[%s] %s (%s) - score: %d%%\n", status, res.FailureName, res.FailureID, scorePercent)

		if !res.Passed {
			var details []string
			if !res.KeywordPass {
				details = append(details, "Keywords: x")
			}
			if !res.DiagnosisPass {
				details = append(details, "Diagnosis: x")
			}
			if !res.ToolEvidence {
				details = append(details, "Tools: x")
			}
			if res.Error != "" {
				details = append(details, fmt.Sprintf("Error: %s", res.Error))
			}
			if len(details) > 0 {
				fmt.Printf("       %s\n", strings.Join(details, " | "))
			}
		}
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Total: %d | Passed: %d | Failed: %d | Rate: %d%%\n",
		r.Summary.Total, r.Summary.Passed, r.Summary.Failed,
		int(r.Summary.PassRate*100))

	for name, stat := range r.Summary.Categories {
		fmt.Printf("  %s: %d/%d (%d%%)\n", name, stat.Passed, stat.Total, int(stat.Rate*100))
	}
	fmt.Println()
}

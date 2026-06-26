package main

import (
	"fmt"
	"math"
	"strings"
)

const (
	stabilityPassThreshold = 0.80 // category agreement rate required for STABLE
	stabilityConfThreshold = 0.30 // max allowed primary-confidence range (30pp)
)

// StabilityReport summarises the consistency of N repeat runs for a single fault.
// Confidence stats cover passing runs only — a failing run may be highly confident
// in a wrong conclusion, which would corrupt the range without adding useful signal.
type StabilityReport struct {
	FailureID          string
	FailureName        string
	N                  int
	PassCount          int
	ConfMin            float64
	ConfMax            float64
	ConfMean           float64
	ProtocolViolations int
	hasConf            bool // true when at least one passing run emitted structured confidence
}

func buildStabilityReport(f Failure, results []EvalResult) StabilityReport {
	r := StabilityReport{
		FailureID:   f.ID,
		FailureName: f.Name,
		N:           len(results),
		ConfMin:     1.0,
	}
	var confSum float64
	var confCount int
	for _, res := range results {
		if res.Passed {
			r.PassCount++
		}
		if res.ProtocolViolation {
			r.ProtocolViolations++
		}
		// Confidence stats: passing runs only.
		if res.Passed && res.PrimaryConfidence > 0 {
			if !r.hasConf || res.PrimaryConfidence < r.ConfMin {
				r.ConfMin = res.PrimaryConfidence
			}
			if res.PrimaryConfidence > r.ConfMax {
				r.ConfMax = res.PrimaryConfidence
			}
			confSum += res.PrimaryConfidence
			confCount++
			r.hasConf = true
		}
	}
	if confCount > 0 {
		r.ConfMean = confSum / float64(confCount)
	}
	return r
}

func (r StabilityReport) passRate() float64 {
	if r.N == 0 {
		return 0
	}
	return float64(r.PassCount) / float64(r.N)
}

func (r StabilityReport) failCount() int { return r.N - r.PassCount }

func (r StabilityReport) confRange() float64 {
	if !r.hasConf {
		return 0
	}
	return r.ConfMax - r.ConfMin
}

func (r StabilityReport) isStable() bool {
	return r.passRate() >= stabilityPassThreshold &&
		(!r.hasConf || r.confRange() <= stabilityConfThreshold)
}

// Print writes the stability report to stdout.
func (r StabilityReport) Print() {
	const width = 64
	sep := strings.Repeat("─", width)
	fmt.Printf("\n  Stability report (%d runs):\n", r.N)

	passRatePct := int(math.Round(r.passRate() * 100))
	passStr := fmt.Sprintf("%d/%d (%d%%)", r.PassCount, r.N, passRatePct)
	if r.passRate() < stabilityPassThreshold {
		passStr += fmt.Sprintf("  [UNSTABLE: want >= %d%%]", int(stabilityPassThreshold*100))
	}
	fmt.Printf("    Pass rate:    %s\n", passStr)

	if r.failCount() > 0 {
		fmt.Printf("    [WARN] %d failed run(s) excluded from confidence stats\n", r.failCount())
	}

	if r.hasConf {
		rangePP := int(math.Round(r.confRange() * 100))
		confStr := fmt.Sprintf("min=%d%% max=%d%% range=%dpp mean=%d%%  (H1, passing runs only)",
			int(math.Round(r.ConfMin*100)),
			int(math.Round(r.ConfMax*100)),
			rangePP,
			int(math.Round(r.ConfMean*100)),
		)
		if r.confRange() > stabilityConfThreshold {
			confStr += fmt.Sprintf("  [UNSTABLE: want <= %dpp]", int(stabilityConfThreshold*100))
		}
		fmt.Printf("    Confidence:   %s\n", confStr)
	} else if r.PassCount > 0 {
		fmt.Printf("    Confidence:   n/a (no passing run emitted structured confidence)\n")
	}

	if r.ProtocolViolations > 0 {
		fmt.Printf("    Violations:   %d protocol violation(s)\n", r.ProtocolViolations)
	}

	if r.isStable() {
		fmt.Printf("    Verdict:      STABLE\n")
	} else {
		fmt.Printf("    Verdict:      UNSTABLE\n")
	}
	fmt.Printf("  %s\n", sep)
}

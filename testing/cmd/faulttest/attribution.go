package main

import (
	"context"
	"math"
	"os"
	"sort"

	"helpdesk/agentutil"
)

// attributionUnknown is a package-local alias for agentutil.AttributionUnknown
// so the many existing references throughout this package don't need renaming.
const attributionUnknown = agentutil.AttributionUnknown

// attributionSummary aggregates per-run attribution classifications at cert time.
type attributionSummary struct {
	PrimaryAttribution      string         // plurality class (UNKNOWN when no majority or all UNKNOWN)
	AttributionConsistent   bool           // all N runs mapped to the same non-UNKNOWN class
	AttributionDistribution map[string]int // label → count, including UNKNOWN
	JudgeSpread             float64        // std dev of DiagnosisScore across all runs (0 when no judge)
	TaxonomyVersion         string         // semver string from root_cause_classes.version
}

// classifyAttribution is a thin package-local alias for agentutil.ClassifyAttribution
// — extracted to a shared package (2026-09) so the gateway can run the identical
// classification against real, organically-resolved incidents. See
// agentutil.ClassifyAttribution's doc comment for the package-boundary reason.
func classifyAttribution(ctx context.Context, completer agentutil.TextCompleter, responseText string, classes []string) string {
	return agentutil.ClassifyAttribution(ctx, completer, responseText, classes)
}

// computeAttributionSummary classifies each eval result's response text and
// aggregates the results into an attributionSummary. A nil completer returns
// a zero-value summary with all UNKNOWN classifications.
func computeAttributionSummary(ctx context.Context, completer agentutil.TextCompleter, results []EvalResult, classes []string, taxonomyVersion string) attributionSummary {
	s := attributionSummary{
		TaxonomyVersion:         taxonomyVersion,
		AttributionDistribution: make(map[string]int),
	}
	if completer == nil || len(results) == 0 {
		s.PrimaryAttribution = attributionUnknown
		return s
	}

	labels := make([]string, 0, len(results))
	for _, r := range results {
		label := classifyAttribution(ctx, completer, r.ResponseText, classes)
		labels = append(labels, label)
		s.AttributionDistribution[label]++
	}

	// Primary = plurality (most frequent non-UNKNOWN); fall back to UNKNOWN.
	type kv struct {
		label string
		count int
	}
	var sorted []kv
	for k, v := range s.AttributionDistribution {
		if k != attributionUnknown {
			sorted = append(sorted, kv{k, v})
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].label < sorted[j].label
	})
	if len(sorted) > 0 {
		s.PrimaryAttribution = sorted[0].label
	} else {
		s.PrimaryAttribution = attributionUnknown
	}

	// Consistent = all runs mapped to same non-UNKNOWN label.
	if s.PrimaryAttribution != attributionUnknown {
		s.AttributionConsistent = true
		for _, l := range labels {
			if l != s.PrimaryAttribution {
				s.AttributionConsistent = false
				break
			}
		}
	}

	// JudgeSpread = std dev of DiagnosisScore.
	if len(results) > 1 {
		var sum float64
		for _, r := range results {
			sum += r.DiagnosisScore
		}
		mean := sum / float64(len(results))
		var varSum float64
		for _, r := range results {
			d := r.DiagnosisScore - mean
			varSum += d * d
		}
		s.JudgeSpread = math.Sqrt(varSum / float64(len(results)))
	}

	return s
}

// newAttributionCompleter creates a TextCompleter for the attribution classifier.
// Uses the judge API key (same as HELPDESK_API_KEY) and Haiku for low cost.
// Returns nil when no API key is available — callers must tolerate a nil completer.
func newAttributionCompleter(ctx context.Context, cfg *HarnessConfig) agentutil.TextCompleter {
	apiKey := cfg.JudgeAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("HELPDESK_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	completer, err := agentutil.NewTextCompleter(ctx, agentutil.Config{
		ModelVendor: "anthropic",
		ModelName:   "claude-haiku-4-5-20251001",
		APIKey:      apiKey,
	})
	if err != nil {
		return nil
	}
	return completer
}

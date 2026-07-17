package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"helpdesk/agentutil"
)

const attributionUnknown = "UNKNOWN"

// attributionSummary aggregates per-run attribution classifications at cert time.
type attributionSummary struct {
	PrimaryAttribution     string         // plurality class (UNKNOWN when no majority or all UNKNOWN)
	AttributionConsistent  bool           // all N runs mapped to the same non-UNKNOWN class
	AttributionDistribution map[string]int // label → count, including UNKNOWN
	JudgeSpread            float64        // std dev of DiagnosisScore across all runs (0 when no judge)
	TaxonomyVersion        string         // semver string from root_cause_classes.version
}

// classifyAttribution calls a cheap LLM completer to map response text to one
// of the provided root-cause classes. Returns UNKNOWN when the LLM output does
// not match any class in the closed list.
func classifyAttribution(ctx context.Context, completer agentutil.TextCompleter, responseText string, classes []string) string {
	if len(classes) == 0 || responseText == "" {
		return attributionUnknown
	}

	classList := strings.Join(classes, "\n  - ")
	prompt := fmt.Sprintf(`You are classifying a triage agent's diagnostic response into exactly one root-cause category.

Allowed categories (return one of these exact strings, nothing else):
  - %s
  - %s

Agent response to classify:
---
%s
---

Instructions:
- Read the FINDINGS and ROOT_CAUSE lines carefully.
- Return exactly one string from the allowed list above that best matches the root cause described.
- If the response does not clearly match any category, return exactly: %s
- Return ONLY the category string. No explanation, no punctuation, no other text.`,
		classList, attributionUnknown, responseText, attributionUnknown)

	out, err := completer(ctx, prompt)
	if err != nil {
		return attributionUnknown
	}
	label := strings.TrimSpace(out)

	// Validate against allowed list.
	for _, c := range classes {
		if strings.EqualFold(label, c) {
			return c // return canonical casing from the list
		}
	}
	if strings.EqualFold(label, attributionUnknown) {
		return attributionUnknown
	}
	return attributionUnknown // non-matching output → treat as unknown
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

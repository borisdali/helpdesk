package agentutil

import (
	"context"
	"fmt"
	"strings"
)

// AttributionUnknown is returned when a diagnostic response cannot be
// confidently mapped to any class in a closed root-cause taxonomy.
const AttributionUnknown = "UNKNOWN"

// ClassifyAttribution calls a cheap LLM completer to map response text to one
// of the provided root-cause classes. Returns AttributionUnknown when the LLM
// output does not match any class in the closed list, when classes is empty,
// or when responseText is empty.
//
// Shared between testing/cmd/faulttest (classifying fault-injection responses
// against a fault's known root-cause taxonomy at cert time) and the gateway
// (classifying real, organically-resolved incidents against their entry
// playbook's own root_cause_classes at resolution time — see
// cmd/gateway/playbooks.go's classifyIncidentAttribution). Two separate main
// packages need the identical logic, so it lives here rather than in either.
func ClassifyAttribution(ctx context.Context, completer TextCompleter, responseText string, classes []string) string {
	if len(classes) == 0 || responseText == "" {
		return AttributionUnknown
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
		classList, AttributionUnknown, responseText, AttributionUnknown)

	out, err := completer(ctx, prompt)
	if err != nil {
		return AttributionUnknown
	}
	label := strings.TrimSpace(out)

	// Validate against allowed list.
	for _, c := range classes {
		if strings.EqualFold(label, c) {
			return c // return canonical casing from the list
		}
	}
	if strings.EqualFold(label, AttributionUnknown) {
		return AttributionUnknown
	}
	return AttributionUnknown // non-matching output → treat as unknown
}

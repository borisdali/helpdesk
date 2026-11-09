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
	return ClassifyAttributionDescribed(ctx, completer, responseText, classes, nil)
}

// ClassifyAttributionDescribed is ClassifyAttribution with a description of
// what each class means (keyed by class name; a class may have none), put in
// front of the classifier. The names of a taxonomy can be read more than one
// way, and the description is where a playbook says which.
func ClassifyAttributionDescribed(ctx context.Context, completer TextCompleter, responseText string, classes []string, descriptions map[string]string) string {
	if len(classes) == 0 || responseText == "" {
		return AttributionUnknown
	}

	prompt := attributionPrompt(responseText, classes, descriptions)
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

// attributionPrompt is what the classifier is asked. A class with a description
// is listed with it; one without is listed by name alone.
func attributionPrompt(responseText string, classes []string, descriptions map[string]string) string {
	var list strings.Builder
	for _, c := range classes {
		list.WriteString("  - " + c)
		if d := strings.Join(strings.Fields(descriptions[c]), " "); d != "" {
			list.WriteString(": " + d)
		}
		list.WriteString("\n")
	}
	return fmt.Sprintf(`You are classifying a triage agent's diagnostic response into exactly one root-cause category.

Allowed categories (return one of these exact category names, nothing else; a description after the colon says what the category means and is not part of the name):
%s  - %s

Agent response to classify:
---
%s
---

Instructions:
- Classify the root cause the agent concluded: its ROOT_CAUSE line and primary hypothesis. Use the FINDINGS to tell two categories apart, not to look for a different one.
- Warnings, alerts, policy or objective verdicts (such as a recovery objective that would be exceeded), and steps a person still has to take before acting are not part of the root cause. Do not let them move the answer to another category.
- Return exactly one name from the allowed list above that best matches the root cause described.
- If the response does not clearly match any category, return exactly: %s
- Return ONLY the category name. No explanation, no punctuation, no other text.`,
		list.String(), AttributionUnknown, responseText, AttributionUnknown)
}

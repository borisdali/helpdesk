package evidence

import (
	"fmt"
	"strconv"
	"strings"

	"helpdesk/internal/audit"
)

// HopOutcome is the gateway's parsed view of a hop's response — the
// model-output-side counterpart to a tool's typed result on the
// evidence-probe side above. Where a ToolSchema[T] probe reads a tool's
// structured return value, a confirmation probe reads this.
type HopOutcome struct {
	// Report is the parsed HYPOTHESIS_N/ROOT_CAUSE/CONFIDENCE block, or nil
	// if the response never emitted one.
	Report *audit.DiagnosticReport
	// RawText is the full, pre-signal-strip response text.
	RawText string
	// SawSignalLine is true iff a TRANSITION_TO:/ESCALATE_TO: line was
	// present at all — see agentEscalation.SawSignalLine in cmd/gateway.
	SawSignalLine bool
}

// confirmationProbe mirrors erasedProbe, but reads a HopOutcome instead of a
// tool-result item, and also receives the specific ObjectiveEvidence being
// confirmed (e.g. evidence_quote_contains_value needs ev.Value).
type confirmationProbe struct {
	kind Kind
	fn   func(HopOutcome, audit.ObjectiveEvidence) any
}

// confirmationRegistry is intentionally a single, fixed, non-generic map —
// unlike the tool-side registry (keyed by tool, since every tool has its own
// result type), there is only ever one shape of HopOutcome to probe, so no
// per-caller registration step is needed; every probe is available to every
// rule via its Signal's ConfirmationProbe field.
var confirmationRegistry = map[string]confirmationProbe{
	"evidence_quote_contains_value": {kind: KindBool, fn: evidenceQuoteContainsValue},
	"resource_named_in_quote":       {kind: KindBool, fn: resourceNamedInQuote},
	"primary_confidence": {kind: KindNumeric, fn: func(h HopOutcome, _ audit.ObjectiveEvidence) any {
		return primaryConfidence(h.Report)
	}},
}

// primaryHypothesis returns the hypothesis marked IsPrimary, or nil if
// report is nil or no hypothesis is marked primary.
func primaryHypothesis(report *audit.DiagnosticReport) *audit.DiagnosticHypothesis {
	if report == nil {
		return nil
	}
	for i := range report.Hypotheses {
		if report.Hypotheses[i].IsPrimary {
			return &report.Hypotheses[i]
		}
	}
	return nil
}

// primaryConfidence returns the primary hypothesis's self-reported
// confidence, or 0 if there is none — mirrors cmd/gateway/playbooks.go's
// lowConfidenceForceGate treating "no primary marked" as maximally
// uncertain, not as an absence of a signal.
func primaryConfidence(report *audit.DiagnosticReport) float64 {
	h := primaryHypothesis(report)
	if h == nil {
		return 0
	}
	return h.Confidence
}

// evidenceQuoteContainsValue reports whether the primary hypothesis's own
// required verbatim EVIDENCE quote contains a string form of ev.Value.
// Scoped deliberately to that one field, not the whole response — it's the
// one part of the protocol already required to be "a short verbatim quote
// from tool output" (see the triage-template.yaml Final-step rules), so a
// real match there means the model demonstrably saw the actual data behind
// the signal, not just that it wrote something plausible-sounding nearby.
func evidenceQuoteContainsValue(h HopOutcome, ev audit.ObjectiveEvidence) any {
	hyp := primaryHypothesis(h.Report)
	if hyp == nil || hyp.Evidence == "" || ev.Value == nil {
		return false
	}
	quote := strings.ToLower(hyp.Evidence)
	for _, form := range valueStrings(ev.Value) {
		if form != "" && strings.Contains(quote, strings.ToLower(form)) {
			return true
		}
	}
	return false
}

// valueStrings returns every reasonable string rendering of v worth
// searching for — a numeric value gets both its integer form (the common
// case: byte counts, seconds, restart counts are all whole numbers in
// practice, and a model is far more likely to quote "95475440" than
// "9.5475440e+07") and its default %v form as a fallback.
func valueStrings(v any) []string {
	switch t := v.(type) {
	case float64:
		var out []string
		if t == float64(int64(t)) {
			out = append(out, strconv.FormatInt(int64(t), 10))
		}
		out = append(out, strconv.FormatFloat(t, 'g', -1, 64))
		return out
	case bool:
		return []string{strconv.FormatBool(t)}
	case string:
		return []string{t}
	default:
		return []string{fmt.Sprintf("%v", t)}
	}
}

// resourceNamedInQuote reports whether ev.Resource (e.g. a pod name, a
// replication slot name) appears anywhere in the hop's response text.
// Fallback for signals whose fired Value is unnatural to quote (a bare
// "true"/"false" for a boolean probe) — resource identifiers are specific
// enough that searching the whole response, not just the EVIDENCE field,
// doesn't meaningfully raise false-positive risk. Empty Resource can never
// be confirmed this way — conservative by construction, not a caller bug.
func resourceNamedInQuote(h HopOutcome, ev audit.ObjectiveEvidence) any {
	if ev.Resource == "" {
		return false
	}
	return strings.Contains(strings.ToLower(h.RawText), strings.ToLower(ev.Resource))
}

// Confirmed reports whether outcome satisfies ev's confirmation rule.
// ev.ConfirmationProbe/Operator/Threshold are authored on the evidence.Rule
// that fired ev and carried through onto the event (see Evaluate) — Confirmed
// itself needs no file access, only the fixed confirmationRegistry above.
// An empty ConfirmationProbe defaults to ("evidence_quote_contains_value",
// "==", true). An unknown probe name (e.g. a gateway running an older
// binary than the agent that posted the event) returns an error; callers
// should treat that as unconfirmed — fail toward the more conservative,
// pre-existing gate behavior, not toward silently trusting an evidence rule
// this process doesn't recognize.
func Confirmed(outcome HopOutcome, ev audit.ObjectiveEvidence) (bool, error) {
	name := ev.ConfirmationProbe
	if name == "" {
		name = "evidence_quote_contains_value"
	}
	probe, ok := confirmationRegistry[name]
	if !ok {
		return false, fmt.Errorf("evidence: unknown confirmation probe %q", name)
	}
	operator := ev.ConfirmationOperator
	if operator == "" {
		operator = "=="
	}
	threshold := ev.ConfirmationThreshold
	if threshold == nil {
		threshold = true
	}
	val := probe.fn(outcome, ev)
	return compare(probe.kind, val, operator, threshold)
}

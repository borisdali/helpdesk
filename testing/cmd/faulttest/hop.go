package main

import (
	"context"
	"sort"

	"helpdesk/internal/audit"
)

// remediationSignature is a fixed sentinel used as every remediation hop's
// attribution signature, regardless of its actual content — remediation is
// always the chain's terminal action, so there is no distinct "which target
// did it hand off to" conclusion for it to diverge on across reps the way an
// escalation hop's EscalatedTo can. Using the same constant every time makes
// buildHopAttribution's generic "all signatures identical" rule naturally
// yield AttributionConsistent=true for remediation hops, with no special
// case needed: f.Remediation.PlaybookID/AgentPrompt are static catalog
// config fixed at author time, so there is genuinely nothing that could vary
// rep to rep.
const remediationSignature = "__remediation__"

// hopSignature is the outcome-validity/attribution/CLEAN proxy computed from
// one narrativeEscalationHop or the narrative's Remediation chapter, for one
// rep (v0.26 item 6 — every hop in a diagnosis chain earns its own stability
// cert, not just the fault's declared entry-point series).
type hopSignature struct {
	SeriesID          string
	Passed            bool   // outcome-validity proxy — see hopOutcomeVerdict
	Signature         string // EscalatedTo value | "resolved" | remediationSignature
	IsTerminal        bool   // true for the Remediation chapter
	Mismatch          bool
	TargetDrift       bool
	ProtocolViolation bool
}

// hopOutcomeVerdict maps a hop's raw Outcome string to the pass-criterion
// table chosen for v1 (outcome-validity, not text-scoring — see project
// memory for why this was chosen over two stricter, deferred alternatives):
//
//	resolved, escalated, transitioned  -> passed=true
//	unknown, abandoned                 -> passed=false
//	gate_pending                       -> exclude=true (this rep doesn't count
//	                                       toward N for this hop at all — a
//	                                       different, legitimate gate paused
//	                                       the chain here; counting it either
//	                                       way corrupts the signal)
//	anything else (future/unrecognized) -> exclude=true, treated the same as
//	                                       gate_pending: conservative, not a
//	                                       silent pass or fail on unknown data
func hopOutcomeVerdict(outcome string) (passed, exclude bool) {
	switch outcome {
	case audit.OutcomeResolved, audit.OutcomeEscalated, audit.OutcomeTransitioned:
		return true, false
	case audit.OutcomeGatePending:
		return false, true
	case audit.OutcomeUnknown, audit.OutcomeAbandoned:
		return false, false
	default:
		return false, true
	}
}

// hopSignatureFor returns the attribution signature for an escalation hop:
// its EscalatedTo target when set, else "resolved" — covers Outcome ==
// resolved/transitioned, and the (data-inconsistent but handled gracefully)
// case of Outcome == escalated with no recorded target.
func hopSignatureFor(escalatedTo string) string {
	if escalatedTo != "" {
		return escalatedTo
	}
	return "resolved"
}

// extractHopSignatures walks one incidentNarrative's Escalations[] and
// Remediation (if present) into hopSignatures, excluding the entry point's
// own series (already certified via the normal per-fault mechanism),
// deduping same-series repeats within this one narrative to the first
// occurrence (a chain can legitimately revisit the same series twice within
// one run — fetchEscalationHops's cycle guard keys on run_id, not
// series_id — and counting both would inflate N without representing two
// independent --repeat exercises), and excluding gate_pending hops entirely.
// Pure function, no I/O.
func extractHopSignatures(n *incidentNarrative, entryPointSeriesID string) []hopSignature {
	if n == nil {
		return nil
	}

	var sigs []hopSignature
	seen := map[string]bool{}

	for _, hop := range n.Escalations {
		if hop.Playbook == "" || hop.Playbook == entryPointSeriesID || seen[hop.Playbook] {
			continue
		}
		passed, exclude := hopOutcomeVerdict(hop.Outcome)
		if exclude {
			continue
		}
		seen[hop.Playbook] = true
		sigs = append(sigs, hopSignature{
			SeriesID:          hop.Playbook,
			Passed:            passed,
			Signature:         hopSignatureFor(hop.EscalatedTo),
			Mismatch:          hop.HasMismatch,
			TargetDrift:       hop.HasTargetDrift,
			ProtocolViolation: hop.HasProtocolViolation,
		})
	}

	if r := n.Remediation; r != nil && r.Playbook != "" && r.Playbook != entryPointSeriesID && !seen[r.Playbook] {
		if passed, exclude := hopOutcomeVerdict(r.Outcome); !exclude {
			sigs = append(sigs, hopSignature{
				SeriesID:          r.Playbook,
				Passed:            passed,
				Signature:         remediationSignature,
				IsTerminal:        true,
				Mismatch:          r.HasMismatch,
				TargetDrift:       r.HasTargetDrift,
				ProtocolViolation: r.HasProtocolViolation,
			})
		}
	}

	return sigs
}

// accumulateHopResults folds one rep's hopSignatures into the running
// per-series accumulators: synthetic EvalResults (reused as-is by
// buildStabilityReport/buildCleanReport — see postHopCerts) and a parallel
// list of attribution signatures per series (see buildHopAttribution).
func accumulateHopResults(acc map[string][]EvalResult, attrSigs map[string][]string, sigs []hopSignature) {
	for _, s := range sigs {
		acc[s.SeriesID] = append(acc[s.SeriesID], EvalResult{
			Passed:            s.Passed,
			Mismatch:          s.Mismatch,
			TargetDrift:       s.TargetDrift,
			ProtocolViolation: s.ProtocolViolation,
		})
		attrSigs[s.SeriesID] = append(attrSigs[s.SeriesID], s.Signature)
	}
}

// buildHopAttribution turns one series' accumulated attribution signatures
// into an attributionSummary via the same "all signatures identical" rule
// documented on hopSignature — computed without an LLM call (contrast with
// computeAttributionSummary in attribution.go, which classifies
// ResponseText via classifyAttribution and IS LLM-based; deliberately not
// reused here to avoid adding a per-hop-per-rep LLM call).
//
// Tie-break for PrimaryAttribution mirrors computeAttributionSummary's own
// convention (attribution.go): highest count wins, lexicographically
// smallest label breaks ties.
func buildHopAttribution(sigs []string) attributionSummary {
	dist := make(map[string]int, len(sigs))
	for _, s := range sigs {
		dist[s]++
	}

	keys := make([]string, 0, len(dist))
	for k := range dist {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	primary := ""
	best := -1
	for _, k := range keys {
		if dist[k] > best {
			best = dist[k]
			primary = k
		}
	}

	return attributionSummary{
		PrimaryAttribution:      primary,
		AttributionConsistent:   len(dist) <= 1,
		AttributionDistribution: dist,
	}
}

// postHopCerts posts one FaultStabilityCert per accumulated series, using a
// synthetic Failure per hop (id = f.ID + "::hop:" + seriesID) so
// postStabilityCert/buildStabilityReport/buildCleanReport can be reused
// completely unchanged — they only ever read the synthetic Failure's own
// ID/Name/DiagnosisPlaybookSeriesID, never the entry-point fault directly.
// This is required, not a convenience: postStabilityCert derives
// playbook_series_id (and the playbook-version staleness stamp) straight
// from the Failure it's given, so passing the original entry-point f here
// (even with just a renamed fault_id) would silently stamp every hop cert
// with the entry point's own series/version instead of the hop's.
//
// Series whose accumulated N is 0 (every rep excluded as gate_pending) are
// skipped — the auditd endpoint rejects n_runs < 1.
func postHopCerts(ctx context.Context, cfg *HarnessConfig, f Failure, acc map[string][]EvalResult, attrSigs map[string][]string) {
	seriesIDs := make([]string, 0, len(acc))
	for id := range acc {
		seriesIDs = append(seriesIDs, id)
	}
	sort.Strings(seriesIDs)

	for _, seriesID := range seriesIDs {
		results := acc[seriesID]
		if len(results) == 0 {
			continue
		}
		synthetic := Failure{
			ID:                        f.ID + "::hop:" + seriesID,
			Name:                      f.Name + " (hop: " + seriesID + ")",
			DiagnosisPlaybookSeriesID: seriesID,
		}
		sr := buildStabilityReport(synthetic, results)
		cr := buildCleanReport(synthetic, results)
		attr := buildHopAttribution(attrSigs[seriesID])
		postStabilityCert(ctx, cfg, synthetic, sr, cr, &attr)
	}
}

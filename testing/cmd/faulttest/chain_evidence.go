package main

import (
	"log/slog"
	"strings"

	"helpdesk/testing/faultlib"
)

// Scoring a diagnosis across a whole chain (v0.33).
//
// Evaluate scores the first hop's response. For a fault whose playbook is
// the start of a multi-hop chain that is the wrong place to look: the entry
// hop is deliberately limited (pbs_db_data_loss_triage has no log tools, so
// it front-loads what it can decide and hands off), and the evidence a
// fault's keywords and category describe ("could not find the database
// system", pg_control, data directory) is found by the hops after it. Scoring
// only hop 1 made a chain that restored the database fail its diagnostic,
// depending on how the entry hop happened to word its hand-off.
//
// ApplyChainEvidence re-checks the keyword and category criteria against the
// text of every diagnostic hop of the chain the run actually took. It only
// ever raises a score; a first hop that already passed is left alone.

// chainDiagnosticText returns the text of the diagnostic hops after the
// first: the findings and primary hypothesis of each intermediate hop. The
// triage hop's own findings are included too. The terminal remediation hop is
// left out on purpose: whether the fix worked is graded by the remediation
// score, and its findings say "restore" simply because it restored.
// hops is the number of hops past the first that contributed, 0 when the run
// was a single hop (nothing to add).
func chainDiagnosticText(n *incidentNarrative) (text string, hops int) {
	if n == nil || (len(n.Escalations) == 0 && n.Remediation == nil) {
		return "", 0
	}
	var b strings.Builder
	add := func(findings string, report *narrativeDiagReport) {
		if s := strings.TrimSpace(findings); s != "" {
			b.WriteString(s)
			b.WriteString("\n")
		}
		if report == nil {
			return
		}
		for _, h := range report.Hypotheses {
			if h.IsPrimary && strings.TrimSpace(h.Text) != "" {
				b.WriteString(strings.TrimSpace(h.Text))
				b.WriteString("\n")
			}
		}
	}
	add(n.Triage.Findings, n.Triage.DiagnosticReport)
	for _, e := range n.Escalations {
		add(e.Findings, e.DiagnosticReport)
		hops++
	}
	return b.String(), hops
}

// keywordCheck is the any_of keyword criterion Evaluate applies.
func keywordCheck(f Failure, lower string) (score float64, pass bool) {
	if len(f.Evaluation.ExpectedKeywords.AnyOf) == 0 {
		return 1.0, true
	}
	for _, kw := range f.Evaluation.ExpectedKeywords.AnyOf {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return 1.0, true
		}
	}
	return 0, false
}

// categoryCheck is the diagnosis-category criterion Evaluate applies: the
// fraction of the category's words present, passing at half or more.
func categoryCheck(f Failure, lower string) (score float64, pass bool) {
	if f.Evaluation.ExpectedDiagnosis.Category == "" {
		return 1.0, true
	}
	words := faultlib.SplitCategory(f.Evaluation.ExpectedDiagnosis.Category)
	if len(words) == 0 {
		return 0, false
	}
	matched := 0
	for _, w := range words {
		if strings.Contains(lower, strings.ToLower(w)) {
			matched++
		}
	}
	ratio := float64(matched) / float64(len(words))
	if ratio >= 0.5 {
		return ratio, true
	}
	return 0, false
}

// ApplyChainEvidence re-checks r's keyword and category criteria against the
// first hop's response plus chain, the text of the later diagnostic hops (see
// chainDiagnosticText), and raises r's scores if the chain meets criteria the
// first hop alone missed. It reports whether anything changed.
//
// It never lowers a score or turns a pass into a fail. A result that an
// evidence gate already failed (EvidenceCoverageGap, EvidenceRequiredButUnconfirmed)
// stays failed, and the protocol-violation cap still applies. With the LLM
// judge in use (judged: it ran and did not skip; JudgeSkipped alone cannot say,
// it is false when no judge was configured at all) the diagnosis score is the
// judge's, so only the keyword criterion is re-checked.
func ApplyChainEvidence(f Failure, r *EvalResult, chain string, hops int, judged bool) bool {
	if chain == "" || r == nil {
		return false
	}
	lower := strings.ToLower(r.ResponseText + "\n" + chain)

	kwScore, kwPass := keywordCheck(f, lower)
	changed := false
	if kwScore > r.KeywordScore {
		r.KeywordScore = kwScore
		r.KeywordPass = kwPass
		changed = true
	}

	if !judged {
		if dScore, dPass := categoryCheck(f, lower); dScore > r.DiagnosisScore {
			r.DiagnosisScore = dScore
			r.DiagnosisPass = dPass
			changed = true
		}
	}
	if !changed {
		return false
	}

	prevScore, prevPassed := r.Score, r.Passed
	if judged {
		r.Score = r.ToolScore*0.40 + r.DiagnosisScore*0.40 + r.KeywordScore*0.20
	} else {
		r.Score = r.KeywordScore*0.5 + r.DiagnosisScore*0.3 + r.ToolScore*0.2
	}
	if r.ProtocolViolation {
		const protocolViolationCap = 0.75
		if r.Score > protocolViolationCap {
			r.Score = protocolViolationCap
		}
	}
	judgeVeto := judged && r.DiagnosisScore < 0.33
	gated := r.EvidenceCoverageGap || r.EvidenceRequiredButUnconfirmed
	r.Passed = prevPassed || (r.Score >= 0.6 && r.KeywordPass && r.OrderingPass && !judgeVeto && !gated)
	if r.Score < prevScore { // belt and braces: a chain can only help
		r.Score = prevScore
	}
	r.ChainEvidenceHops = hops
	return true
}

// applyChainEvidenceFromGateway fetches the finished run's incident narrative
// and applies ApplyChainEvidence to r. It reports the number of later
// diagnostic hops that were counted and whether the score changed. A fetch
// failure is logged and leaves r untouched: the diagnosis is then scored on
// the first hop only, as before chain evidence existed. When the score
// changes, the overall score is recomputed with the remediation score the
// run already has.
func applyChainEvidenceFromGateway(gatewayURL, apiKey string, f Failure, r *EvalResult, runID string, judged bool) (hops int, changed bool) {
	narrative, err := fetchIncidentNarrative(gatewayURL, apiKey, runID)
	if err != nil {
		slog.Warn("chain evidence: could not fetch the incident narrative; the diagnosis is scored on the first hop only", "run_id", runID, "err", err)
		return 0, false
	}
	chain, hops := chainDiagnosticText(narrative)
	if !ApplyChainEvidence(f, r, chain, hops, judged) {
		return hops, false
	}
	if r.RemediationAttempted {
		r.OverallScore = r.Score*0.6 + r.RemediationScore*0.4
	} else {
		r.OverallScore = r.Score
	}
	return hops, true
}

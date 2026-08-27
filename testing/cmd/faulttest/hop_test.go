package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"helpdesk/testing/faultlib"
)

// ── hopOutcomeVerdict ────────────────────────────────────────────────────

func TestHopOutcomeVerdict(t *testing.T) {
	tests := []struct {
		outcome     string
		wantPassed  bool
		wantExclude bool
	}{
		{"resolved", true, false},
		{"escalated", true, false},
		{"transitioned", true, false},
		{"unknown", false, false},
		{"abandoned", false, false},
		{"gate_pending", false, true},
		{"escalated+resolved", false, true}, // top-level-only synthetic value, never on a hop; treated conservatively
		{"some_future_outcome", false, true},
		{"", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			passed, exclude := hopOutcomeVerdict(tt.outcome)
			if passed != tt.wantPassed || exclude != tt.wantExclude {
				t.Errorf("hopOutcomeVerdict(%q) = (%v, %v), want (%v, %v)",
					tt.outcome, passed, exclude, tt.wantPassed, tt.wantExclude)
			}
		})
	}
}

func TestHopSignatureFor(t *testing.T) {
	if got := hopSignatureFor("pbs_k8s_pod_crash_triage"); got != "pbs_k8s_pod_crash_triage" {
		t.Errorf("hopSignatureFor(target) = %q, want target", got)
	}
	if got := hopSignatureFor(""); got != "resolved" {
		t.Errorf("hopSignatureFor(\"\") = %q, want %q", got, "resolved")
	}
}

// ── extractHopSignatures ─────────────────────────────────────────────────

func TestExtractHopSignatures_Nil(t *testing.T) {
	if got := extractHopSignatures(nil, "pbs_entry"); got != nil {
		t.Errorf("extractHopSignatures(nil, ...) = %v, want nil", got)
	}
}

func TestExtractHopSignatures_BasicEscalation(t *testing.T) {
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{Playbook: "pbs_sysadmin_docker_inspect", Outcome: "escalated", EscalatedTo: "pbs_k8s_pod_crash_triage"},
		},
	}
	sigs := extractHopSignatures(n, "pbs_db_restart_triage")
	if len(sigs) != 1 {
		t.Fatalf("len = %d, want 1", len(sigs))
	}
	got := sigs[0]
	if got.SeriesID != "pbs_sysadmin_docker_inspect" {
		t.Errorf("SeriesID = %q", got.SeriesID)
	}
	if !got.Passed {
		t.Error("Passed should be true for Outcome=escalated")
	}
	if got.Signature != "pbs_k8s_pod_crash_triage" {
		t.Errorf("Signature = %q, want the escalation target", got.Signature)
	}
	if got.IsTerminal {
		t.Error("IsTerminal should be false for an escalation hop")
	}
}

func TestExtractHopSignatures_ExcludesEntryPoint(t *testing.T) {
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{Playbook: "pbs_db_restart_triage", Outcome: "escalated", EscalatedTo: "pbs_sysadmin_docker_inspect"},
			{Playbook: "pbs_sysadmin_docker_inspect", Outcome: "resolved"},
		},
	}
	sigs := extractHopSignatures(n, "pbs_db_restart_triage")
	if len(sigs) != 1 || sigs[0].SeriesID != "pbs_sysadmin_docker_inspect" {
		t.Errorf("expected only the non-entry-point hop, got %+v", sigs)
	}
}

func TestExtractHopSignatures_ExcludesGatePending(t *testing.T) {
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{Playbook: "pbs_sysadmin_docker_inspect", Outcome: "gate_pending"},
		},
	}
	sigs := extractHopSignatures(n, "pbs_entry")
	if len(sigs) != 0 {
		t.Errorf("gate_pending hop should be excluded entirely, got %+v", sigs)
	}
}

func TestExtractHopSignatures_DedupSameSeries(t *testing.T) {
	// A chain can legitimately revisit the same series twice within one run
	// (fetchEscalationHops's cycle guard keys on run_id, not series_id) —
	// only the first occurrence should count.
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{Playbook: "pbs_sysadmin_docker_inspect", Outcome: "escalated", EscalatedTo: "pbs_k8s_pod_crash_triage"},
			{Playbook: "pbs_sysadmin_docker_inspect", Outcome: "resolved"},
		},
	}
	sigs := extractHopSignatures(n, "pbs_entry")
	if len(sigs) != 1 {
		t.Fatalf("len = %d, want 1 (deduped)", len(sigs))
	}
	if sigs[0].Signature != "pbs_k8s_pod_crash_triage" {
		t.Errorf("expected the FIRST occurrence's signature to win, got %q", sigs[0].Signature)
	}
}

func TestExtractHopSignatures_UnknownAndAbandonedFail(t *testing.T) {
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{Playbook: "pbs_a", Outcome: "unknown"},
			{Playbook: "pbs_b", Outcome: "abandoned"},
		},
	}
	sigs := extractHopSignatures(n, "pbs_entry")
	if len(sigs) != 2 {
		t.Fatalf("len = %d, want 2", len(sigs))
	}
	for _, s := range sigs {
		if s.Passed {
			t.Errorf("series %s: Passed should be false for unknown/abandoned", s.SeriesID)
		}
	}
}

func TestExtractHopSignatures_CarriesCleanSignals(t *testing.T) {
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{
				Playbook: "pbs_sysadmin_docker_inspect", Outcome: "resolved",
				HasMismatch: true, HasTargetDrift: true, HasProtocolViolation: true,
			},
		},
	}
	sigs := extractHopSignatures(n, "pbs_entry")
	if len(sigs) != 1 {
		t.Fatalf("len = %d, want 1", len(sigs))
	}
	got := sigs[0]
	if !got.Mismatch || !got.TargetDrift || !got.ProtocolViolation {
		t.Errorf("CLEAN signals not carried through: %+v", got)
	}
}

func TestExtractHopSignatures_RemediationChapter(t *testing.T) {
	n := &incidentNarrative{
		Remediation: &struct {
			RunID                string          `json:"run_id"`
			Playbook             string          `json:"playbook"`
			Outcome              string          `json:"outcome"`
			Findings             string          `json:"findings,omitempty"`
			Transcript           string          `json:"transcript,omitempty"`
			Steps                []narrativeStep `json:"steps,omitempty"`
			TraceID              string          `json:"trace_id,omitempty"`
			HasMismatch          bool            `json:"has_mismatch,omitempty"`
			HasTargetDrift       bool            `json:"has_target_drift,omitempty"`
			HasProtocolViolation bool            `json:"has_protocol_violation,omitempty"`
			SawSignalLine        bool            `json:"saw_signal_line,omitempty"`
		}{
			Playbook: "pbs_k8s_pod_crash_remediate",
			Outcome:  "resolved",
		},
	}
	sigs := extractHopSignatures(n, "pbs_entry")
	if len(sigs) != 1 {
		t.Fatalf("len = %d, want 1", len(sigs))
	}
	got := sigs[0]
	if got.SeriesID != "pbs_k8s_pod_crash_remediate" {
		t.Errorf("SeriesID = %q", got.SeriesID)
	}
	if !got.IsTerminal {
		t.Error("IsTerminal should be true for the Remediation chapter")
	}
	if got.Signature != remediationSignature {
		t.Errorf("Signature = %q, want the fixed remediation sentinel", got.Signature)
	}
}

func TestExtractHopSignatures_RemediationDedupedAgainstEscalations(t *testing.T) {
	// Same series reached both as an escalation hop and (in a self-loop) as
	// the remediation target — the escalation occurrence should win.
	n := &incidentNarrative{
		Escalations: []narrativeEscalationHop{
			{Playbook: "pbs_shared", Outcome: "escalated", EscalatedTo: "pbs_other"},
		},
		Remediation: &struct {
			RunID                string          `json:"run_id"`
			Playbook             string          `json:"playbook"`
			Outcome              string          `json:"outcome"`
			Findings             string          `json:"findings,omitempty"`
			Transcript           string          `json:"transcript,omitempty"`
			Steps                []narrativeStep `json:"steps,omitempty"`
			TraceID              string          `json:"trace_id,omitempty"`
			HasMismatch          bool            `json:"has_mismatch,omitempty"`
			HasTargetDrift       bool            `json:"has_target_drift,omitempty"`
			HasProtocolViolation bool            `json:"has_protocol_violation,omitempty"`
			SawSignalLine        bool            `json:"saw_signal_line,omitempty"`
		}{
			Playbook: "pbs_shared",
			Outcome:  "resolved",
		},
	}
	sigs := extractHopSignatures(n, "pbs_entry")
	if len(sigs) != 1 {
		t.Fatalf("len = %d, want 1 (deduped across Escalations/Remediation)", len(sigs))
	}
	if sigs[0].IsTerminal {
		t.Error("the escalation occurrence should win, not the remediation one")
	}
}

// ── accumulateHopResults ─────────────────────────────────────────────────

func TestAccumulateHopResults(t *testing.T) {
	acc := map[string][]EvalResult{}
	attrSigs := map[string][]string{}

	accumulateHopResults(acc, attrSigs, []hopSignature{
		{SeriesID: "pbs_a", Passed: true, Signature: "pbs_b", TargetDrift: true},
	})
	accumulateHopResults(acc, attrSigs, []hopSignature{
		{SeriesID: "pbs_a", Passed: false, Signature: "resolved"},
	})

	if len(acc["pbs_a"]) != 2 {
		t.Fatalf("acc[pbs_a] len = %d, want 2", len(acc["pbs_a"]))
	}
	if !acc["pbs_a"][0].Passed || !acc["pbs_a"][0].TargetDrift {
		t.Errorf("first EvalResult wrong: %+v", acc["pbs_a"][0])
	}
	if acc["pbs_a"][1].Passed {
		t.Error("second EvalResult should have Passed=false")
	}
	if len(attrSigs["pbs_a"]) != 2 || attrSigs["pbs_a"][0] != "pbs_b" || attrSigs["pbs_a"][1] != "resolved" {
		t.Errorf("attrSigs[pbs_a] = %v, want [pbs_b resolved]", attrSigs["pbs_a"])
	}
}

// ── buildHopAttribution ──────────────────────────────────────────────────

func TestBuildHopAttribution_AllConsistent(t *testing.T) {
	attr := buildHopAttribution([]string{"pbs_k8s", "pbs_k8s", "pbs_k8s"})
	if !attr.AttributionConsistent {
		t.Error("AttributionConsistent should be true when every rep escalated to the same target")
	}
	if attr.PrimaryAttribution != "pbs_k8s" {
		t.Errorf("PrimaryAttribution = %q", attr.PrimaryAttribution)
	}
}

func TestBuildHopAttribution_ResolvesSometimesEscalatesOtherTimes(t *testing.T) {
	// The one case that actually matters: the hop reached genuinely different
	// conclusions about whether the problem needs further escalation at all.
	attr := buildHopAttribution([]string{"resolved", "pbs_k8s", "resolved"})
	if attr.AttributionConsistent {
		t.Error("AttributionConsistent should be false when the hop sometimes resolves, sometimes escalates further")
	}
	if attr.PrimaryAttribution != "resolved" {
		t.Errorf("PrimaryAttribution = %q, want the plurality label \"resolved\" (2 of 3)", attr.PrimaryAttribution)
	}
}

func TestBuildHopAttribution_TieBreakLexicographic(t *testing.T) {
	attr := buildHopAttribution([]string{"pbs_zeta", "pbs_alpha"})
	if attr.PrimaryAttribution != "pbs_alpha" {
		t.Errorf("PrimaryAttribution = %q, want lexicographically smallest on a tie", attr.PrimaryAttribution)
	}
	if attr.AttributionConsistent {
		t.Error("two distinct signatures should not be consistent")
	}
}

func TestBuildHopAttribution_Empty(t *testing.T) {
	attr := buildHopAttribution(nil)
	if !attr.AttributionConsistent {
		t.Error("empty signature list should be vacuously consistent")
	}
	if attr.PrimaryAttribution != "" {
		t.Errorf("PrimaryAttribution = %q, want empty", attr.PrimaryAttribution)
	}
}

func TestBuildHopAttribution_RemediationAlwaysConsistent(t *testing.T) {
	// remediationSignature is the same fixed sentinel every time, regardless
	// of rep count — this is what makes remediation-hop attribution "always
	// true" fall out of the generic rule with no special case.
	attr := buildHopAttribution([]string{remediationSignature, remediationSignature, remediationSignature})
	if !attr.AttributionConsistent {
		t.Error("remediation hops should always be attribution-consistent")
	}
}

// ── postHopCerts ─────────────────────────────────────────────────────────

func TestPostHopCerts_PostsOnePerSeries(t *testing.T) {
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// playbook-version stamp lookup inside postStabilityCert — fail
			// gracefully, not under test here.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"playbooks":[]}`)) //nolint:errcheck
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		posted = append(posted, body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{
		HarnessConfig:  faultlib.HarnessConfig{GatewayURL: srv.URL},
		DiagnosisModel: "claude-sonnet-4-6",
	}
	f := Failure{ID: "db-wal-disk-full-k8s", Name: "WAL disk full", DiagnosisPlaybookSeriesID: "pbs_db_restart_triage"}

	acc := map[string][]EvalResult{
		"pbs_sysadmin_docker_inspect": {{Passed: true}, {Passed: true}},
		"pbs_k8s_pod_crash_triage":    {{Passed: true}, {Passed: false}},
	}
	attrSigs := map[string][]string{
		"pbs_sysadmin_docker_inspect": {"pbs_k8s_pod_crash_triage", "pbs_k8s_pod_crash_triage"},
		"pbs_k8s_pod_crash_triage":    {"resolved", "resolved"},
	}

	postHopCerts(context.Background(), cfg, f, acc, attrSigs)

	if len(posted) != 2 {
		t.Fatalf("posted %d certs, want 2", len(posted))
	}

	byFaultID := map[string]map[string]any{}
	for _, b := range posted {
		byFaultID[b["fault_id"].(string)] = b
	}

	sysadmin, ok := byFaultID["db-wal-disk-full-k8s::hop:pbs_sysadmin_docker_inspect"]
	if !ok {
		t.Fatalf("no cert posted for the synthetic sysadmin hop fault_id; got fault_ids: %v", keysOf(byFaultID))
	}
	if sysadmin["playbook_series_id"] != "pbs_sysadmin_docker_inspect" {
		t.Errorf("sysadmin hop playbook_series_id = %v, want its OWN series, not the entry point's", sysadmin["playbook_series_id"])
	}
	if sysadmin["n_runs"] != float64(2) {
		t.Errorf("sysadmin hop n_runs = %v, want 2", sysadmin["n_runs"])
	}

	k8s, ok := byFaultID["db-wal-disk-full-k8s::hop:pbs_k8s_pod_crash_triage"]
	if !ok {
		t.Fatalf("no cert posted for the synthetic k8s hop fault_id")
	}
	if k8s["playbook_series_id"] != "pbs_k8s_pod_crash_triage" {
		t.Errorf("k8s hop playbook_series_id = %v, want pbs_k8s_pod_crash_triage", k8s["playbook_series_id"])
	}
}

func TestPostHopCerts_SkipsEmptySeries(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"playbooks":[]}`)) //nolint:errcheck
			return
		}
		posts++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &HarnessConfig{HarnessConfig: faultlib.HarnessConfig{GatewayURL: srv.URL}, DiagnosisModel: "m"}
	f := Failure{ID: "f1", Name: "F1"}
	acc := map[string][]EvalResult{"pbs_never_populated": {}}
	postHopCerts(context.Background(), cfg, f, acc, map[string][]string{})

	if posts != 0 {
		t.Errorf("posts = %d, want 0 — a series with N=0 must be skipped", posts)
	}
}

func keysOf(m map[string]map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ── fetchHopCerts / vaultHopCerts ────────────────────────────────────────

func TestFetchHopCerts_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") != "pbs_sysadmin_docker_inspect" || r.URL.Query().Get("model") != "claude-sonnet-4-6" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"certs": []map[string]any{
				{"fault_id": "db-wal-disk-full-k8s::hop:pbs_sysadmin_docker_inspect", "is_stable": true, "is_clean": true, "n_runs": 3},
			},
		})
	}))
	defer srv.Close()

	certs := fetchHopCerts(srv.URL, "", "pbs_sysadmin_docker_inspect", "claude-sonnet-4-6")
	if len(certs) != 1 {
		t.Fatalf("len = %d, want 1", len(certs))
	}
	if certs[0].FaultID != "db-wal-disk-full-k8s::hop:pbs_sysadmin_docker_inspect" {
		t.Errorf("FaultID = %q", certs[0].FaultID)
	}
	if !certs[0].IsStable || !certs[0].IsClean {
		t.Error("expected IsStable/IsClean true")
	}
}

func TestFetchHopCerts_EmptyArgsReturnNil(t *testing.T) {
	if got := fetchHopCerts("", "key", "pbs_x", "model"); got != nil {
		t.Error("empty gatewayURL should return nil")
	}
	if got := fetchHopCerts("http://x", "key", "", "model"); got != nil {
		t.Error("empty seriesID should return nil")
	}
	if got := fetchHopCerts("http://x", "key", "pbs_x", ""); got != nil {
		t.Error("empty model should return nil")
	}
}

func TestFetchHopCerts_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if got := fetchHopCerts(srv.URL, "", "pbs_x", "model"); got != nil {
		t.Errorf("got %v, want nil on 500", got)
	}
}

func TestFetchHopCerts_SendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"certs": []map[string]any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	fetchHopCerts(srv.URL, "secret-key", "pbs_x", "model")
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-key")
	}
}

func TestVaultHopCerts_CertsFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"certs": []map[string]any{
				{
					"fault_id":   "db-wal-disk-full-k8s::hop:pbs_sysadmin_docker_inspect",
					"fault_name": "WAL disk full (hop: pbs_sysadmin_docker_inspect)",
					"is_stable":  true, "is_clean": true, "attribution_consistent": true,
					"primary_attribution": "pbs_k8s_pod_crash_triage",
					"n_runs":              3, "pass_rate": 1.0,
				},
			},
		})
	}))
	defer srv.Close()

	out := captureStdout(func() {
		vaultHopCerts([]string{"pbs_sysadmin_docker_inspect", "--gateway", srv.URL, "--agent-model", "claude-sonnet-4-6"})
	})

	if !strings.Contains(out, "db-wal-disk-full-k8s::hop:pbs_sysadmin_docker_inspect") {
		t.Errorf("output missing fault_id:\n%s", out)
	}
	if !strings.Contains(out, "EARNED") {
		t.Errorf("output missing trust verdict (all three EarnsTrust conditions true):\n%s", out)
	}
	if !strings.Contains(out, "STABLE") || !strings.Contains(out, "CLEAN") {
		t.Errorf("output missing stability/clean verdicts:\n%s", out)
	}
}

func TestVaultHopCerts_NoneFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"certs": []map[string]any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	out := captureStdout(func() {
		vaultHopCerts([]string{"pbs_never_certified", "--gateway", srv.URL, "--agent-model", "claude-sonnet-4-6"})
	})

	if !strings.Contains(out, "never been certified") {
		t.Errorf("output missing the no-certs guidance message:\n%s", out)
	}
	if !strings.Contains(out, "--repeat") {
		t.Errorf("output missing the --repeat workflow hint:\n%s", out)
	}
}

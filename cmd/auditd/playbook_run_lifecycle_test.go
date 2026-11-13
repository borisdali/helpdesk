package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

// fullPlaybookRunServer has every store auditd wires, in one database: the stats
// and calibration queries join them, so a server with fewer cannot answer them.
func fullPlaybookRunServer(t *testing.T) *playbookRunServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	prs, err := audit.NewPlaybookRunStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	pbs, err := audit.NewPlaybookStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	fbs, err := audit.NewRunFeedbackStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := audit.NewRunEvaluationStore(store.DB(), false)
	if err != nil {
		t.Fatal(err)
	}
	// The version stats count steps, so the table that holds them must exist.
	if _, err := audit.NewPlaybookRunStepStore(store.DB(), false); err != nil {
		t.Fatal(err)
	}
	return &playbookRunServer{store: prs, playbookStore: pbs, feedbackStore: fbs, evaluationStore: evs}
}

func recordRun(t *testing.T, srv *playbookRunServer, id, series, prior, outcome string) {
	t.Helper()
	body := map[string]any{"run_id": id, "series_id": series, "prior_run_id": prior, "outcome": outcome}
	if rec := doRunRequest(t, srv, "pb_"+id, body); rec.Code != http.StatusCreated {
		t.Fatalf("record %s: %d %s", id, rec.Code, rec.Body.String())
	}
}

func TestPlaybookRunHandlers_GetRun(t *testing.T) {
	srv := newPlaybookRunServer(t)
	recordRun(t, srv, "plr_g1", "pbs_vacuum_triage", "", "")
	rec := callPV(srv.handleGetRun, http.MethodGet, "/x", nil, map[string]string{"runID": "plr_g1"})
	var got audit.PlaybookRun
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.RunID != "plr_g1" || got.SeriesID != "pbs_vacuum_triage" {
		t.Errorf("%d %+v", rec.Code, got)
	}
	if rec := callPV(srv.handleGetRun, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleGetRun, http.MethodGet, "/x", nil, map[string]string{"runID": "plr_nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", rec.Code)
	}
}

func TestPlaybookRunHandlers_ListByOutcomeSeriesAndPrior(t *testing.T) {
	srv := newPlaybookRunServer(t)
	recordRun(t, srv, "plr_l1", "pbs_vacuum_triage", "", "resolved")
	recordRun(t, srv, "plr_l2", "pbs_vacuum_triage", "plr_l1", "escalated")
	recordRun(t, srv, "plr_l3", "pbs_lock_chain_triage", "", "resolved")

	ids := func(query string) []string {
		t.Helper()
		rec := callPV(srv.handleListByOutcome, http.MethodGet, "/v1/fleet/playbook-runs"+query, nil, nil)
		var out struct {
			Runs  []audit.PlaybookRun `json:"runs"`
			Count int                 `json:"count"`
		}
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil || out.Count != len(out.Runs) {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var got []string
		for _, r := range out.Runs {
			got = append(got, r.RunID)
		}
		return got
	}
	has := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for _, w := range want {
			if !strings.Contains(strings.Join(got, ","), w) {
				return false
			}
		}
		return true
	}
	if got := ids("?series_id=pbs_vacuum_triage"); !has(got, "plr_l1", "plr_l2") {
		t.Errorf("by series = %v", got)
	}
	if got := ids("?prior_run_id=plr_l1"); !has(got, "plr_l2") {
		t.Errorf("by prior run = %v", got)
	}
	if got := ids("?outcome=resolved"); !has(got, "plr_l1", "plr_l3") {
		t.Errorf("by outcome = %v", got)
	}
	if got := ids("?outcome=resolved&limit=1"); len(got) != 1 {
		t.Errorf("limit = %v", got)
	}
	if got := ids("?outcome=resolved&limit=banana"); len(got) != 2 {
		t.Errorf("an unreadable limit should be ignored: %v", got)
	}
	// The series wins over the others; a listing with no filter is refused.
	if got := ids("?series_id=pbs_lock_chain_triage&outcome=resolved"); !has(got, "plr_l3") {
		t.Errorf("series should win: %v", got)
	}
	if got := ids("?outcome=nothing-has-this"); len(got) != 0 {
		t.Errorf("no match = %v", got)
	}
	rec := callPV(srv.handleListByOutcome, http.MethodGet, "/v1/fleet/playbook-runs", nil, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "series_id, prior_run_id, or outcome") {
		t.Errorf("no filter: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybookRunHandlers_Update_Errors(t *testing.T) {
	srv := newPlaybookRunServer(t)
	if rec := callPV(srv.handleUpdate, http.MethodPatch, "/x", map[string]any{"outcome": "resolved"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleUpdate, http.MethodPatch, "/x", "{nope", map[string]string{"runID": "plr_x"}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Errorf("not JSON: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybookRunHandlers_VersionStats_Branches(t *testing.T) {
	srv := fullPlaybookRunServer(t)
	if rec := callPV(srv.handleVersionStats, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no series: %d", rec.Code)
	}
	rec := callPV(srv.handleVersionStats, http.MethodGet, "/x", nil, map[string]string{"seriesID": "pbs_unknown"})
	var out struct {
		SeriesID string `json:"series_id"`
		Versions []any  `json:"versions"`
	}
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil || out.SeriesID != "pbs_unknown" || out.Versions == nil || len(out.Versions) != 0 {
		t.Errorf("a series with no runs: %d %s (versions must be a list, not null)", rec.Code, rec.Body.String())
	}
}

// With no feedback or evaluation store configured, every route that needs one says so.
func TestPlaybookRunHandlers_StoresNotConfigured(t *testing.T) {
	srv := newPlaybookRunServer(t) // no feedback store, no evaluation store
	pv := map[string]string{"runID": "plr_x"}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"submit feedback":   callPV(srv.handleSubmitFeedback, http.MethodPost, "/x", map[string]any{}, pv),
		"get feedback":      callPV(srv.handleGetFeedback, http.MethodGet, "/x", nil, pv),
		"pending feedback":  callPV(srv.handleListPendingFeedback, http.MethodGet, "/x", nil, nil),
		"submit evaluation": callPV(srv.handleSubmitEvaluation, http.MethodPost, "/x", map[string]any{}, pv),
		"get evaluation":    callPV(srv.handleGetEvaluation, http.MethodGet, "/x", nil, pv),
		"calibration":       callPV(srv.handleCalibration, http.MethodGet, "/x", nil, nil),
		"fault run history": callPV(srv.handleFaultRunHistory, http.MethodGet, "/x", nil, nil),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with no store = %d, want 503", name, rec.Code)
		}
	}
}

func TestPlaybookRunHandlers_Feedback_Branches(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)
	if rec := callPV(srv.handleSubmitFeedback, http.MethodPost, "/x", map[string]any{}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("submit, no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleSubmitFeedback, http.MethodPost, "/x", "{nope", map[string]string{"runID": "plr_x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("submit, not JSON: %d", rec.Code)
	}
	if rec := callPV(srv.handleGetFeedback, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("get, no id: %d", rec.Code)
	}

	// The submitter defaults to the X-User header, and the series to the run's own.
	recordRun(t, srv, "plr_f1", "pbs_lock_chain_triage", "", "")
	req := callPVWithHeader(srv.handleSubmitFeedback, "plr_f1", map[string]any{"feedback_type": "triage", "feedback_time": "post_incident", "verdict_correct": true}, "X-User", "dana")
	var fb audit.RunFeedback
	if req.Code != http.StatusCreated || json.NewDecoder(req.Body).Decode(&fb) != nil || fb.Operator != "dana" || fb.SeriesID != "pbs_lock_chain_triage" || fb.RunID != "plr_f1" {
		t.Fatalf("%d %+v", req.Code, fb)
	}
	// A body that names an operator keeps it.
	req = callPVWithHeader(srv.handleSubmitFeedback, "plr_f1", map[string]any{"feedback_type": "remediation", "feedback_time": "at_gate", "operator": "erin"}, "X-User", "dana")
	if json.NewDecoder(req.Body).Decode(&fb) != nil || fb.Operator != "erin" {
		t.Errorf("operator = %q, want erin", fb.Operator)
	}

	// A filter with only one half defaults the other: triage, post_incident.
	rec := callPV(srv.handleGetFeedback, http.MethodGet, "/x?feedback_time=post_incident", nil, map[string]string{"runID": "plr_f1"})
	var got audit.RunFeedback
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.FeedbackType != "triage" || got.FeedbackTime != "post_incident" {
		t.Errorf("time only: %d %+v", rec.Code, got)
	}
	rec = callPV(srv.handleGetFeedback, http.MethodGet, "/x?feedback_type=remediation", nil, map[string]string{"runID": "plr_f1"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("type only defaults the time to post_incident, where there is no remediation feedback: %d", rec.Code)
	}
	// The unfiltered form lists everything on the run.
	rec = callPV(srv.handleGetFeedback, http.MethodGet, "/x", nil, map[string]string{"runID": "plr_f1"})
	var all struct {
		Feedback []audit.RunFeedback `json:"feedback"`
	}
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&all) != nil || len(all.Feedback) != 2 {
		t.Errorf("unfiltered: %d %+v", rec.Code, all)
	}
}

func callPVWithHeader(h http.HandlerFunc, runID string, body any, hk, hv string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(data))
	req.SetPathValue("runID", runID)
	req.Header.Set(hk, hv)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestPlaybookRunHandlers_Evaluation_Branches(t *testing.T) {
	srv := newPlaybookRunServerWithEvaluation(t)
	if rec := callPV(srv.handleSubmitEvaluation, http.MethodPost, "/x", map[string]any{}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("submit, no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleSubmitEvaluation, http.MethodPost, "/x", "{nope", map[string]string{"runID": "plr_x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("submit, not JSON: %d", rec.Code)
	}
	if rec := callPV(srv.handleGetEvaluation, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("get, no id: %d", rec.Code)
	}
}

func TestPlaybookRunHandlers_Calibration_And_History_Branches(t *testing.T) {
	srv := fullPlaybookRunServer(t)
	if rec := callPV(srv.handleCalibration, http.MethodGet, "/v1/fleet/calibration?series_id=pbs_x", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("calibration scoped to a series: %d %s", rec.Code, rec.Body.String())
	}
	rec := callPV(srv.handleFaultRunHistory, http.MethodGet, "/v1/fleet/fault-run-history?since_days=7&fault_id=db-x", nil, nil)
	var out struct {
		Entries []any `json:"entries"`
	}
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil || out.Entries == nil {
		t.Errorf("history: %d %s (entries must be a list, not null)", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"?since_days=banana", "?since_days=-3", "?since_days=0"} {
		if rec := callPV(srv.handleFaultRunHistory, http.MethodGet, "/x"+q, nil, nil); rec.Code != http.StatusOK {
			t.Errorf("an unreadable since_days (%s) should be ignored: %d", q, rec.Code)
		}
	}
}

// ── govbot ────────────────────────────────────────────────────────────────────

func newGovbotServer(t *testing.T) *govbotServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	gs, err := audit.NewGovbotStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatal(err)
	}
	return &govbotServer{store: gs}
}

func TestGovbotHandlers_SaveAndList(t *testing.T) {
	srv := newGovbotServer(t)
	if rec := callPV(srv.handleSaveRun, http.MethodPost, "/v1/govbot/runs", "{nope", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("not JSON: %d", rec.Code)
	}
	for i := 0; i < 3; i++ {
		rec := callPV(srv.handleSaveRun, http.MethodPost, "/v1/govbot/runs", map[string]any{"gateway": "http://gw-a"}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
	}
	callPV(srv.handleSaveRun, http.MethodPost, "/v1/govbot/runs", map[string]any{"gateway": "http://gw-b"}, nil)

	count := func(query string) int {
		t.Helper()
		rec := callPV(srv.handleGetRuns, http.MethodGet, "/v1/govbot/runs"+query, nil, nil)
		var out []audit.GovbotRun
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		return len(out)
	}
	if got := count(""); got != 4 {
		t.Errorf("all = %d, want 4", got)
	}
	if got := count("?gateway=http://gw-a"); got != 3 {
		t.Errorf("one gateway = %d, want 3", got)
	}
	if got := count("?limit=2"); got != 2 {
		t.Errorf("limit = %d, want 2", got)
	}
	if got := count("?limit=zero"); got != 4 {
		t.Errorf("an unreadable limit should be ignored: %d", got)
	}

	// ?retain keeps only the newest for that gateway, and a bad value keeps all.
	callPV(srv.handleSaveRun, http.MethodPost, "/v1/govbot/runs?retain=2", map[string]any{"gateway": "http://gw-a"}, nil)
	if got := count("?gateway=http://gw-a"); got != 2 {
		t.Errorf("after retain=2: %d, want 2", got)
	}
	callPV(srv.handleSaveRun, http.MethodPost, "/v1/govbot/runs?retain=banana", map[string]any{"gateway": "http://gw-a"}, nil)
	if got := count("?gateway=http://gw-a"); got != 3 {
		t.Errorf("an unreadable retain should prune nothing: %d, want 3", got)
	}
}

func TestGovbotHandlers_EmptyIsAListNotNull(t *testing.T) {
	srv := newGovbotServer(t)
	rec := callPV(srv.handleGetRuns, http.MethodGet, "/v1/govbot/runs", nil, nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
}

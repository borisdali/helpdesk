package audit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryApproval_Approved(t *testing.T) {
	full := RecoveryApproval{Type: "time", Value: "2026-10-27T14:05:00Z", PlanDigest: "0123456789abcdef", ApprovedBy: "alice", Acknowledged: true}
	if !full.Approved() {
		t.Fatal("a complete approval must be approved")
	}
	// Each piece is required on its own.
	for name, a := range map[string]RecoveryApproval{
		"no digest":   {Type: "time", Value: "x", ApprovedBy: "alice", Acknowledged: true},
		"no approver": {Type: "time", Value: "x", PlanDigest: "d", Acknowledged: true},
		"no ack":      {Type: "time", Value: "x", PlanDigest: "d", ApprovedBy: "alice"},
		"no target":   {PlanDigest: "d", ApprovedBy: "alice", Acknowledged: true},
	} {
		if a.Approved() {
			t.Errorf("%s: approved", name)
		}
	}
	var none *RecoveryApproval
	if none.Approved() {
		t.Error("nil approved")
	}
}

func TestParseRecoveryApproval(t *testing.T) {
	good := map[string]any{"type": "lsn", "value": " 0/3000028 ", "plan_digest": "abc", "approved_by": "alice", "acknowledged": true}
	a := parseRecoveryApproval(good)
	if a == nil || a.Type != "lsn" || a.Value != "0/3000028" || !a.Approved() {
		t.Fatalf("parsed = %+v", a)
	}
	// A run that only has a target to plan: parsed, not approved.
	if p := parseRecoveryApproval(map[string]any{"type": "time", "value": "2026-10-27T14:05:00Z"}); p == nil || p.Approved() {
		t.Errorf("target without approval: %+v", p)
	}
	for name, v := range map[string]any{"nil": nil, "string": "time=x", "list": []any{"a"}, "no value": map[string]any{"type": "time"}, "wrong types": map[string]any{"type": 1, "value": 2}} {
		if parseRecoveryApproval(v) != nil {
			t.Errorf("%s: want nil", name)
		}
	}
	// acknowledged must be a real boolean, not a truthy string.
	if parseRecoveryApproval(map[string]any{"type": "time", "value": "x", "plan_digest": "d", "approved_by": "a", "acknowledged": "true"}).Approved() {
		t.Error(`acknowledged:"true" (a string) counted as an acknowledgement`)
	}
}

func TestRecoveryTargetFormat(t *testing.T) {
	if got := FormatRecoveryTarget("time", "2026-10-27T14:05:00Z"); got != "time=2026-10-27T14:05:00Z" {
		t.Errorf("got %q", got)
	}
	if FormatRecoveryTarget("", "x") != "" || FormatRecoveryTarget("time", "") != "" {
		t.Error("an incomplete target must format as empty")
	}
	if typ, v := ParseRecoveryTarget("lsn=0/3000028"); typ != "lsn" || v != "0/3000028" {
		t.Errorf("parsed %q %q", typ, v)
	}
	// A value may itself contain '=': only the first splits.
	if typ, v := ParseRecoveryTarget("time=a=b"); typ != "time" || v != "a=b" {
		t.Errorf("parsed %q %q", typ, v)
	}
}

// The approval rides the A2A metadata into the request context that tools
// read, through the real middleware.
func TestTraceMiddleware_CarriesRecoveryApproval(t *testing.T) {
	var got *RecoveryApproval
	h := TraceMiddlewareWithAudit(&CurrentTraceStore{}, nil, "sysadmin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = RecoveryApprovalFromContext(r.Context())
	}))
	body := makeA2ABody(t, map[string]any{
		"trace_id":          "tr_x",
		"recovery_approval": map[string]any{"type": "time", "value": "2026-10-27T14:05:00Z", "plan_digest": "d", "approved_by": "alice", "acknowledged": true},
	}, "restore")
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !got.Approved() || got.ApprovedBy != "alice" {
		t.Errorf("approval in the tool context = %+v", got)
	}

	got = &RecoveryApproval{}
	body = makeA2ABody(t, map[string]any{"trace_id": "tr_y"}, "restore")
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if got != nil {
		t.Errorf("a request without an approval must have none, got %+v", got)
	}
}

// A run stores the target it was given and a chained run keeps it; databases
// created before the column existed gain it on open.
func TestPlaybookRunStore_RecoveryTarget(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()
	run := &PlaybookRun{PlaybookID: "pb_1", SeriesID: "pbs_pgbackrest_pitr_triage", ExecutionMode: "agent",
		RecoveryTarget: FormatRecoveryTarget("time", "2026-10-27T14:05:00Z"), StartedAt: time.Now().UTC()}
	if err := s.Record(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByRunID(ctx, run.RunID)
	if err != nil || got.RecoveryTarget != "time=2026-10-27T14:05:00Z" {
		t.Fatalf("GetByRunID = %+v, %v", got, err)
	}
	if typ, v := ParseRecoveryTarget(got.RecoveryTarget); typ != "time" || v != "2026-10-27T14:05:00Z" {
		t.Errorf("parsed %q %q", typ, v)
	}
	// Update must not clear it.
	if err := s.Update(ctx, run.RunID, "escalated", "", "pbs_x", "findings", "", "", nil, false, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetByRunID(ctx, run.RunID); got.RecoveryTarget == "" {
		t.Error("Update cleared the recovery target")
	}
	for name, list := range map[string]func() ([]*PlaybookRun, error){
		"ListByPlaybook": func() ([]*PlaybookRun, error) { return s.ListByPlaybook(ctx, "pb_1", 5) },
		"ListBySeriesID": func() ([]*PlaybookRun, error) { return s.ListBySeriesID(ctx, "pbs_pgbackrest_pitr_triage", 5) },
		"ListByOutcome":  func() ([]*PlaybookRun, error) { return s.ListByOutcome(ctx, "escalated", 5) },
	} {
		runs, err := list()
		if err != nil || len(runs) != 1 || runs[0].RecoveryTarget == "" {
			t.Errorf("%s: %v %v", name, runs, err)
		}
	}
}

// A database created before the column existed gains it when the store opens,
// and its old rows read back with no target.
func TestPlaybookRunStore_RecoveryTargetColumnMigration(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "old.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.DB().Exec(`CREATE TABLE playbook_runs (
    run_id TEXT NOT NULL PRIMARY KEY, playbook_id TEXT NOT NULL, series_id TEXT NOT NULL,
    execution_mode TEXT NOT NULL DEFAULT 'fleet', outcome TEXT NOT NULL DEFAULT 'unknown',
    escalated_to TEXT NOT NULL DEFAULT '', findings_summary TEXT NOT NULL DEFAULT '',
    diagnostic_report TEXT NOT NULL DEFAULT '', context_id TEXT NOT NULL DEFAULT '',
    operator TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO playbook_runs (run_id, playbook_id, series_id, started_at) VALUES ('plr_old', 'pb', 'pbs_x', '2026-10-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	s, err := NewPlaybookRunStore(store.DB(), false)
	if err != nil {
		t.Fatalf("opening the store on an old database: %v", err)
	}
	old, err := s.GetByRunID(context.Background(), "plr_old")
	if err != nil || old.RecoveryTarget != "" {
		t.Fatalf("old row = %+v, %v", old, err)
	}
}

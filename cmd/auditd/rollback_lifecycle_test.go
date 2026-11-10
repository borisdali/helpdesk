package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"helpdesk/internal/audit"
)

// ── the executor ──────────────────────────────────────────────────────────────

type fakeGateway struct {
	mu     sync.Mutex
	calls  []gatewayCall
	status int
	reply  string
}

type gatewayCall struct {
	path, auth, trace, body string
}

func newFakeGateway(t *testing.T, status int, reply string) (*fakeGateway, string) {
	t.Helper()
	g := &fakeGateway{status: status, reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.calls = append(g.calls, gatewayCall{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Trace-ID"), string(b)})
		g.mu.Unlock()
		w.WriteHeader(g.status)
		io.WriteString(w, g.reply) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func executorFixture(t *testing.T, gatewayURL, key string) (*RollbackExecutor, *audit.RollbackStore, *audit.Store) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	rs, err := audit.NewRollbackStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatal(err)
	}
	return NewRollbackExecutor(rs, store, gatewayURL, key), rs, store
}

func scalePlan() audit.RollbackPlan {
	return audit.RollbackPlan{
		OriginalEventID: "tool_1", OriginalTool: "scale_deployment", OriginalTraceID: "tr_orig", Reversibility: audit.ReversibilityYes,
		InverseOp: &audit.InverseOperation{Agent: "k8s", Tool: "scale_deployment",
			Args: map[string]any{"namespace": "production", "deployment": "api", "replicas": 3}, Description: "scale back to 3"},
	}
}

func newRollbackRecord(t *testing.T, rs *audit.RollbackStore, plan any, status string) *audit.RollbackRecord {
	t.Helper()
	pj, _ := json.Marshal(plan)
	if s, ok := plan.(string); ok {
		pj = []byte(s)
	}
	rbk := &audit.RollbackRecord{OriginalEventID: "tool_1", OriginalTraceID: "tr_orig", InitiatedBy: "alice", PlanJSON: string(pj), Status: status}
	if err := rs.CreateRollback(context.Background(), rbk); err != nil {
		t.Fatal(err)
	}
	return rbk
}

func eventsOf(t *testing.T, store *audit.Store, typ audit.EventType) []audit.Event {
	t.Helper()
	evs, err := store.Query(context.Background(), audit.QueryOptions{EventType: typ})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestRollbackExecutor_DispatchesTheInverseOpToTheGateway(t *testing.T) {
	gw, url := newFakeGateway(t, http.StatusOK, `{"scaled":true}`)
	ex, rs, store := executorFixture(t, url, "svc-key")
	rbk := newRollbackRecord(t, rs, scalePlan(), "pending_approval")

	if err := ex.Execute(context.Background(), rbk); err != nil {
		t.Fatal(err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway calls = %+v", gw.calls)
	}
	c := gw.calls[0]
	if c.path != "/api/v1/k8s/scale_deployment" || c.auth != "Bearer svc-key" || c.trace != rbk.RollbackTraceID {
		t.Errorf("call = %+v", c)
	}
	var args map[string]any
	if json.Unmarshal([]byte(c.body), &args) != nil || args["deployment"] != "api" || args["replicas"] != float64(3) {
		t.Errorf("args sent = %s", c.body)
	}
	got, _ := rs.GetRollback(context.Background(), rbk.RollbackID)
	if got.Status != "success" || got.ResultOutput != `{"scaled":true}` {
		t.Errorf("record = %+v", got)
	}
	// Both lifecycle events are on the record, in the rollback's own trace.
	executed, verified := eventsOf(t, store, audit.EventTypeRollbackExecuted), eventsOf(t, store, audit.EventTypeRollbackVerified)
	if len(executed) != 1 || len(verified) != 1 {
		t.Fatalf("events: %d executed, %d verified", len(executed), len(verified))
	}
	if executed[0].TraceID != rbk.RollbackTraceID || executed[0].RollbackExecution == nil || executed[0].RollbackExecution.Status != "success" ||
		executed[0].RollbackExecution.OriginalEventID != "tool_1" {
		t.Errorf("executed event = %+v", executed[0])
	}
}

func TestRollbackExecutor_AGatewayFailureIsAFailedRollback(t *testing.T) {
	_, url := newFakeGateway(t, http.StatusInternalServerError, "kaboom")
	ex, rs, store := executorFixture(t, url, "")
	rbk := newRollbackRecord(t, rs, scalePlan(), "pending_approval")

	err := ex.Execute(context.Background(), rbk)
	if err == nil || !strings.Contains(err.Error(), "gateway returned 500") {
		t.Fatalf("err = %v", err)
	}
	got, _ := rs.GetRollback(context.Background(), rbk.RollbackID)
	if got.Status != "failed" || !strings.Contains(got.ResultOutput, "kaboom") {
		t.Errorf("record = %+v", got)
	}
	// The failure is recorded, and it is not then claimed to have been verified.
	if ex := eventsOf(t, store, audit.EventTypeRollbackExecuted); len(ex) != 1 || ex[0].RollbackExecution.Status != "failed" || ex[0].RollbackExecution.ErrorMessage == "" {
		t.Errorf("executed events = %+v", ex)
	}
	if v := eventsOf(t, store, audit.EventTypeRollbackVerified); len(v) != 0 {
		t.Errorf("a failed rollback was verified: %+v", v)
	}
}

func TestRollbackExecutor_ANoGatewayConfiguredSkipsTheDispatch(t *testing.T) {
	ex, rs, store := executorFixture(t, "", "")
	rbk := newRollbackRecord(t, rs, scalePlan(), "pending_approval")
	if err := ex.Execute(context.Background(), rbk); err != nil {
		t.Fatal(err)
	}
	got, _ := rs.GetRollback(context.Background(), rbk.RollbackID)
	if got.Status != "success" || !strings.Contains(got.ResultOutput, "gateway not configured") {
		t.Errorf("record = %+v", got)
	}
	if len(eventsOf(t, store, audit.EventTypeRollbackExecuted)) != 1 {
		t.Error("the skipped dispatch left no executed event")
	}
}

func TestRollbackExecutor_UnreachableGatewayFails(t *testing.T) {
	ex, rs, _ := executorFixture(t, "http://127.0.0.1:1", "")
	rbk := newRollbackRecord(t, rs, scalePlan(), "pending_approval")
	if err := ex.Execute(context.Background(), rbk); err == nil || !strings.Contains(err.Error(), "gateway request failed") {
		t.Errorf("err = %v", err)
	}
	if got, _ := rs.GetRollback(context.Background(), rbk.RollbackID); got.Status != "failed" {
		t.Errorf("status = %s", got.Status)
	}
}

// A record that is already finished is left alone, so a retry cannot undo twice.
func TestRollbackExecutor_TerminalRecordsAreNotRunAgain(t *testing.T) {
	gw, url := newFakeGateway(t, http.StatusOK, "{}")
	ex, rs, store := executorFixture(t, url, "")
	for _, status := range []string{"success", "failed", "cancelled"} {
		rbk := newRollbackRecord(t, rs, scalePlan(), status)
		if err := ex.Execute(context.Background(), rbk); err != nil {
			t.Errorf("%s: %v", status, err)
		}
	}
	if len(gw.calls) != 0 || len(eventsOf(t, store, audit.EventTypeRollbackExecuted)) != 0 {
		t.Errorf("a finished rollback ran again: %d calls", len(gw.calls))
	}
}

// What cannot be executed is refused before anything is changed.
func TestRollbackExecutor_RefusesWhatIsNotExecutable(t *testing.T) {
	gw, url := newFakeGateway(t, http.StatusOK, "{}")
	ex, rs, _ := executorFixture(t, url, "")
	notReversible := scalePlan()
	notReversible.Reversibility = audit.ReversibilityNo
	noOp := scalePlan()
	noOp.InverseOp = nil
	for name, tc := range map[string]struct {
		plan any
		want string
	}{
		"plan is not JSON":     {"{nope", "unmarshal rollback plan"},
		"not reversible":       {notReversible, "not executable"},
		"no compensating call": {noOp, "not executable"},
	} {
		rbk := newRollbackRecord(t, rs, tc.plan, "pending_approval")
		err := ex.Execute(context.Background(), rbk)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if got, _ := rs.GetRollback(context.Background(), rbk.RollbackID); got.Status != "pending_approval" {
			t.Errorf("%s: the record moved to %s without running", name, got.Status)
		}
	}
	if len(gw.calls) != 0 {
		t.Errorf("a non-executable rollback reached the gateway: %+v", gw.calls)
	}
}

// ── the handlers that were not covered ────────────────────────────────────────

func rollbackWithApprovals(t *testing.T) (*rollbackServer, *audit.Store) {
	t.Helper()
	srv, store := newRollbackTestServer(t)
	as, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatal(err)
	}
	srv.approvalStore = as
	return srv, store
}

func TestRollbackHandlers_Initiate_QueuesAnApprovalAndRecordsTheEvent(t *testing.T) {
	srv, store := rollbackWithApprovals(t)
	eventID := recordScaleEvent(t, store, 3)

	rec := callPV(srv.handleInitiateRollback, http.MethodPost, "/v1/rollbacks", map[string]any{"original_event_id": eventID, "justification": "bad deploy"}, nil)
	var out struct {
		Rollback audit.RollbackRecord `json:"rollback"`
	}
	if rec.Code != http.StatusCreated || json.NewDecoder(rec.Body).Decode(&out) != nil || out.Rollback.ApprovalID == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if out.Rollback.Status != "pending_approval" || out.Rollback.InitiatedBy != "unknown" {
		t.Errorf("rollback = %+v", out.Rollback)
	}
	// The approval is in the queue, linked to the rollback, and says what and why.
	appr, err := srv.approvalStore.GetRequest(context.Background(), out.Rollback.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if appr.ToolName != "rollback" || appr.PolicyName != "rollback-requires-approval" || appr.TraceID != out.Rollback.RollbackTraceID ||
		appr.RequestContext["justification"] != "bad deploy" || appr.RequestContext["rollback_id"] != out.Rollback.RollbackID {
		t.Errorf("approval = %+v", appr)
	}
	if stored, _ := srv.store.GetRollback(context.Background(), out.Rollback.RollbackID); stored.ApprovalID != out.Rollback.ApprovalID {
		t.Errorf("the approval was not linked on the stored record: %q", stored.ApprovalID)
	}
	if evs := eventsOf(t, store, audit.EventTypeRollbackInitiated); len(evs) != 1 || evs[0].RollbackExecution == nil || evs[0].Input.UserQuery != "bad deploy" {
		t.Errorf("initiated events = %+v", evs)
	}
}

func TestRollbackHandlers_Initiate_Errors(t *testing.T) {
	srv, _ := newRollbackTestServer(t)
	if rec := callPV(srv.handleInitiateRollback, http.MethodPost, "/x", "{nope", nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Errorf("not JSON: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callPV(srv.handleInitiateRollback, http.MethodPost, "/x", map[string]any{"original_event_id": "no-such-event"}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown event: %d", rec.Code)
	}
}

func TestRollbackHandlers_List_Filters(t *testing.T) {
	srv, _ := newRollbackTestServer(t)
	mk := func(event, trace, by, status string) {
		t.Helper()
		if err := srv.store.CreateRollback(context.Background(), &audit.RollbackRecord{OriginalEventID: event, OriginalTraceID: trace, InitiatedBy: by, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	mk("e1", "tr_a", "alice", "pending_approval")
	mk("e2", "tr_a", "bob", "success")
	mk("e3", "tr_b", "alice", "failed")
	count := func(query string) int {
		t.Helper()
		rec := callPV(srv.handleListRollbacks, http.MethodGet, "/v1/rollbacks"+query, nil, nil)
		var out []audit.RollbackRecord
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil {
			t.Fatalf("%s: %d", query, rec.Code)
		}
		return len(out)
	}
	for query, want := range map[string]int{
		"": 3, "?original_event_id=e2": 1, "?original_trace_id=tr_a": 2, "?status=failed": 1, "?initiated_by=alice": 2,
		"?initiated_by=alice&status=failed": 1, "?limit=1": 1, "?limit=zero": 3, "?limit=-1": 3,
	} {
		if got := count(query); got != want {
			t.Errorf("list%s = %d, want %d", query, got, want)
		}
	}
}

func TestRollbackHandlers_Get_Branches(t *testing.T) {
	srv, _ := newRollbackTestServer(t)
	if rec := callPV(srv.handleGetRollback, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	// A record whose stored plan is damaged is still returned, with no plan.
	rbk := &audit.RollbackRecord{OriginalEventID: "e1", PlanJSON: "{damaged"}
	if err := srv.store.CreateRollback(context.Background(), rbk); err != nil {
		t.Fatal(err)
	}
	rec := callPV(srv.handleGetRollback, http.MethodGet, "/x", nil, map[string]string{"rollbackID": rbk.RollbackID})
	var out struct {
		Rollback audit.RollbackRecord `json:"rollback"`
		Plan     *audit.RollbackPlan  `json:"plan"`
	}
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil || out.Rollback.RollbackID != rbk.RollbackID || out.Plan != nil {
		t.Errorf("%d %+v", rec.Code, out)
	}
}

func TestRollbackHandlers_Cancel_Branches(t *testing.T) {
	srv, _ := newRollbackTestServer(t)
	if rec := callPV(srv.handleCancelRollback, http.MethodPost, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleCancelRollback, http.MethodPost, "/x", nil, map[string]string{"rollbackID": "rbk_nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
	// One that is running cannot be taken back.
	rbk := &audit.RollbackRecord{OriginalEventID: "e1", PlanJSON: "{}", Status: "executing"}
	if err := srv.store.CreateRollback(context.Background(), rbk); err != nil {
		t.Fatal(err)
	}
	rec := callPV(srv.handleCancelRollback, http.MethodPost, "/x", nil, map[string]string{"rollbackID": rbk.RollbackID})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "currently executing") {
		t.Errorf("executing: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := srv.store.GetRollback(context.Background(), rbk.RollbackID); got.Status != "executing" {
		t.Errorf("the running rollback was changed to %s", got.Status)
	}
}

func TestRollbackHandlers_FleetRollback(t *testing.T) {
	srv, _ := newRollbackTestServer(t)
	job := &audit.FleetJob{Name: "vacuum-prod", SubmittedBy: "fleet-runner", JobDef: "{}"}
	if err := srv.fleetStore.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	pv := map[string]string{"jobID": job.JobID}

	// Refusals.
	if rec := callPV(srv.handleInitiateFleetRollback, http.MethodPost, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleInitiateFleetRollback, http.MethodPost, "/x", nil, map[string]string{"jobID": "flj_nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", rec.Code)
	}
	if rec := callPV(srv.handleGetFleetRollback, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("get, no id: %d", rec.Code)
	}
	if rec := callPV(srv.handleGetFleetRollback, http.MethodGet, "/x", nil, pv); rec.Code != http.StatusNotFound {
		t.Errorf("get, none yet: %d", rec.Code)
	}

	// A dry run says what would happen and writes nothing.
	rec := callPV(srv.handleInitiateFleetRollback, http.MethodPost, "/x", map[string]any{"dry_run": true, "scope": "canary_only"}, pv)
	var dry map[string]any
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&dry) != nil || dry["dry_run"] != true || dry["scope"] != "canary_only" || dry["job_name"] != "vacuum-prod" {
		t.Errorf("dry run: %d %v", rec.Code, dry)
	}
	if rec := callPV(srv.handleGetFleetRollback, http.MethodGet, "/x", nil, pv); rec.Code != http.StatusNotFound {
		t.Errorf("a dry run left a record: %d", rec.Code)
	}

	// For real: scope defaults to all, and an unreadable body is ignored.
	rec = callPV(srv.handleInitiateFleetRollback, http.MethodPost, "/x", "{nope", pv)
	var frb audit.FleetRollbackRecord
	if rec.Code != http.StatusCreated || json.NewDecoder(rec.Body).Decode(&frb) != nil || frb.Scope != "all" || frb.OriginalJobID != job.JobID || frb.InitiatedBy != "unknown" {
		t.Fatalf("create: %d %+v", rec.Code, frb)
	}
	// Read back.
	rec = callPV(srv.handleGetFleetRollback, http.MethodGet, "/x", nil, pv)
	var got audit.FleetRollbackRecord
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.FleetRollbackID != frb.FleetRollbackID {
		t.Errorf("get: %d %+v", rec.Code, got)
	}
	// A second one for the same job is refused, and shows the first.
	rec = callPV(srv.handleInitiateFleetRollback, http.MethodPost, "/x", map[string]any{"scope": "failed_only"}, pv)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "active fleet rollback already exists") || !strings.Contains(rec.Body.String(), frb.FleetRollbackID) {
		t.Errorf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestIsNotFound(t *testing.T) {
	for err, want := range map[error]bool{
		nil:                                   false,
		errors.New("rollback not found"):      true,
		errors.New("x: not found: y"):         true,
		errors.New("database is locked"):      false,
		errors.New("not foun"):                false,
		errors.New("event 12 was not found."): true,
	} {
		if got := isNotFound(err); got != want {
			t.Errorf("isNotFound(%v) = %v, want %v", err, got, want)
		}
	}
	if containsStr("ab", "abc") || !containsStr("abc", "") || !containsStr("abc", "bc") || containsStr("abc", "cb") {
		t.Error("containsStr")
	}
}

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/infra"
)

// drillFakes stands in for the database agent's direct tool endpoint and for
// auditd's drill store, and remembers what each was sent.
type drillFakes struct {
	mu         sync.Mutex
	toolArgs   []map[string]any
	posted     []audit.DrillRecord
	listQuery  []url.Values
	verdict    audit.DrillVerification // what the agent answers
	toolText   string                  // answer with this plain text instead of a verdict
	toolStatus int                     // non-zero: the agent answers with this error status
	auditCode  int                     // non-zero: auditd answers POST with this status
	auditBody  string
}

func drillInfra() *infra.Config {
	return &infra.Config{DBServers: map[string]infra.DBServer{
		"prod-orders":  {Name: "orders", ConnectionString: "host=db1.example.com port=5432 dbname=o user=u password=s3cret", PasswordEnv: "ORDERS_PW", Environment: "prod"},
		"orders-clone": {Name: "orders-clone", ConnectionString: "host=scratch.example.com port=5432 dbname=o user=u", VMName: "scratch-vm"},
		"other":        {Name: "other", ConnectionString: "host=db9.example.com port=5432 dbname=x"},
	}}
}

func goodVerdict() audit.DrillVerification {
	return audit.DrillVerification{
		Output: "Restore drill of prod-orders on orders-clone: VERIFIED\n", Status: audit.DrillVerified,
		Checks:    []audit.DrillCheck{{Name: "same_system", Result: "pass"}, {Name: "archive_guard", Result: "pass"}},
		SourceKey: "prod-orders", CloneKey: "orders-clone",
		SourceSystemID: "7001", CloneSystemID: "7001",
		SourceEndpoint: "db1.example.com:5432", CloneEndpoint: "scratch.example.com:5432",
		SourceBytes: 100 << 20, CloneBytes: 98 << 20,
	}
}

func (f *drillFakes) gateway(t *testing.T) *Gateway {
	t.Helper()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tool/verify_restore_drill" {
			http.NotFound(w, r)
			return
		}
		var body directToolReq
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		f.mu.Lock()
		f.toolArgs = append(f.toolArgs, body.Args)
		f.mu.Unlock()
		if f.toolStatus != 0 {
			http.Error(w, `{"error":"agent exploded"}`, f.toolStatus)
			return
		}
		out := f.toolText
		if out == "" {
			b, _ := json.Marshal(f.verdict)
			out = string(b)
		}
		json.NewEncoder(w).Encode(map[string]string{"output": out}) //nolint:errcheck
	}))
	t.Cleanup(agent.Close)

	aud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/drills":
			var rec audit.DrillRecord
			json.NewDecoder(r.Body).Decode(&rec) //nolint:errcheck
			f.mu.Lock()
			f.posted = append(f.posted, rec)
			f.mu.Unlock()
			if f.auditCode != 0 {
				http.Error(w, f.auditBody, f.auditCode)
				return
			}
			rec.DrillID, rec.AuditEventID = "drl_fake0001", "evt_fake0001"
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(rec) //nolint:errcheck
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/drills"):
			f.mu.Lock()
			f.listQuery = append(f.listQuery, r.URL.Query())
			f.mu.Unlock()
			if r.URL.Path == "/v1/drills/drl_missing" {
				http.Error(w, "drill not found", http.StatusNotFound)
				return
			}
			w.Write([]byte(`[]`)) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(aud.Close)

	return &Gateway{
		auditURL: aud.URL, auditAPIKey: "k",
		infra: drillInfra(),
		agents: map[string]*discovery.Agent{
			agentNameDB: {Name: agentNameDB, InvokeURL: agent.URL + "/invoke"},
		},
	}
}

func drillCall(t *testing.T, gw *Gateway, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func goodDrillRequest() map[string]any {
	fin := time.Now().UTC().Add(-time.Hour)
	return map[string]any{
		"source": "prod-orders", "clone": "orders-clone", "method": "pgbackrest",
		"started_at": fin.Add(-95 * time.Second).Format(time.RFC3339), "finished_at": fin.Format(time.RFC3339),
		"ticket": "DRILL-7", "note": "Q4 drill", "backup_label": "20261101-020000F",
	}
}

func TestRecordDrill_VerifiesThenStoresTheRecord(t *testing.T) {
	f := &drillFakes{verdict: goodVerdict()}
	gw := f.gateway(t)
	req := goodDrillRequest()
	req["restore_bytes"] = 1 // a claim: ignored, the measured size is what is recorded
	rec := drillCall(t, gw, http.MethodPost, "/api/v1/drills", req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(f.toolArgs) != 1 || f.toolArgs[0]["source"] != "prod-orders" || f.toolArgs[0]["clone"] != "orders-clone" {
		t.Errorf("the agent must be given the infra keys, got %v", f.toolArgs)
	}
	if _, has := f.toolArgs[0]["verify_sql"]; has {
		t.Error("no verify_sql was asked for")
	}
	if len(f.posted) != 1 {
		t.Fatalf("posted %d records", len(f.posted))
	}
	p := f.posted[0]
	if p.SourceKey != "prod-orders" || p.CloneKey != "orders-clone" || p.SourceSystemID != "7001" || p.CloneSystemID != "7001" ||
		p.Status != audit.DrillVerified || len(p.Checks) != 2 || p.Method != "pgbackrest" {
		t.Errorf("record: %+v", p)
	}
	if p.DurationSeconds != 95 || p.DurationSource != audit.DrillDurationReported {
		t.Errorf("duration %ds from %q, want 95s reported", p.DurationSeconds, p.DurationSource)
	}
	if p.RestoreBytes != 98<<20 {
		t.Errorf("restore_bytes %d: must be the measured size of the clone", p.RestoreBytes)
	}
	if p.RecordedBy != "anonymous" || p.Ticket != "DRILL-7" || p.Note != "Q4 drill" || p.BackupLabel != "20261101-020000F" {
		t.Errorf("who/ticket/note: %+v", p)
	}
	if p.SourceHostType != "host" || !strings.HasPrefix(p.InfraDigest, "sha256:") {
		t.Errorf("host type %q, digest %q", p.SourceHostType, p.InfraDigest)
	}
	var got struct {
		Drill        audit.DrillRecord `json:"drill"`
		Verification string            `json:"verification"`
	}
	json.NewDecoder(rec.Body).Decode(&got) //nolint:errcheck
	if got.Drill.DrillID != "drl_fake0001" || !strings.Contains(got.Verification, "VERIFIED") {
		t.Errorf("response: %+v", got)
	}
}

func TestRecordDrill_ARefusedCloneIsOnTheTrailToo(t *testing.T) {
	v := goodVerdict()
	v.Status = audit.DrillRefused
	v.Checks = []audit.DrillCheck{{Name: "archive_guard", Result: "fail", Detail: "would push into the source's archive"}}
	f := &drillFakes{verdict: v}
	rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", goodDrillRequest())
	if rec.Code != http.StatusCreated || len(f.posted) != 1 || f.posted[0].Status != audit.DrillRefused {
		t.Fatalf("%d %s; posted %+v", rec.Code, rec.Body.String(), f.posted)
	}
	if !strings.Contains(rec.Body.String(), "refused") {
		t.Errorf("the answer must say it was refused: %s", rec.Body.String())
	}
}

func TestRecordDrill_UnverifiableCloneIsRecordedToo(t *testing.T) {
	v := goodVerdict()
	v.Status = audit.DrillUnverifiable
	v.SourceSystemID, v.CloneSystemID, v.CloneBytes = "", "", 0
	v.Checks = []audit.DrillCheck{{Name: "reachable", Result: "fail", Detail: "the clone could not be reached: connection refused"}}
	f := &drillFakes{verdict: v}
	rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", goodDrillRequest())
	if rec.Code != http.StatusCreated || len(f.posted) != 1 {
		t.Fatalf("%d %s; posted %d", rec.Code, rec.Body.String(), len(f.posted))
	}
	p := f.posted[0]
	if p.Status != audit.DrillUnverifiable || p.CloneSystemID != "" || p.RestoreBytes != 0 || len(p.Checks) != 1 {
		t.Errorf("record %+v", p)
	}
}

func TestRecordDrill_ForwardsSupersedes(t *testing.T) {
	f := &drillFakes{verdict: goodVerdict()}
	req := goodDrillRequest()
	req["supersedes"] = "drl_earlier01"
	if rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", req); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if f.posted[0].Supersedes != "drl_earlier01" {
		t.Errorf("supersedes = %q", f.posted[0].Supersedes)
	}
}

func TestRecordDrill_UnreadableBodiesAreRefusedCleanly(t *testing.T) {
	for name, body := range map[string]any{
		"time not RFC3339": map[string]any{"source": "prod-orders", "clone": "orders-clone", "method": "pgbackrest", "started_at": "yesterday"},
		"duration a word":  map[string]any{"source": "prod-orders", "clone": "orders-clone", "method": "pgbackrest", "duration_seconds": "long"},
		"not an object":    []string{"prod-orders"},
	} {
		f := &drillFakes{verdict: goodVerdict()}
		rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON body") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if len(f.toolArgs) != 0 || len(f.posted) != 0 {
			t.Errorf("%s: something was called or stored", name)
		}
	}
}

func TestRecordDrill_PassesVerifySQLToTheTool(t *testing.T) {
	f := &drillFakes{verdict: goodVerdict()}
	req := goodDrillRequest()
	req["verify_sql"] = "select count(*) > 0 from orders"
	if rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", req); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if f.toolArgs[0]["verify_sql"] != "select count(*) > 0 from orders" {
		t.Errorf("args %v", f.toolArgs[0])
	}
}

func TestRecordDrill_RejectsBeforeTouchingAnything(t *testing.T) {
	cases := map[string]struct {
		edit func(map[string]any)
		code int
		want string
	}{
		"unknown clone":   {func(r map[string]any) { r["clone"] = "new-clone" }, http.StatusNotFound, "has to be registered in the infrastructure config"},
		"unknown source":  {func(r map[string]any) { r["source"] = "nope" }, http.StatusNotFound, "not in the infrastructure configuration"},
		"clone is source": {func(r map[string]any) { r["clone"] = "prod-orders" }, http.StatusBadRequest, "cannot be the source"},
		"no method":       {func(r map[string]any) { delete(r, "method") }, http.StatusBadRequest, "method is required"},
		"bad method":      {func(r map[string]any) { r["method"] = "dump" }, http.StatusBadRequest, "not one of"},
		"bad ticket":      {func(r map[string]any) { r["ticket"] = "not a ticket!" }, http.StatusBadRequest, "not a ticket reference"},
		"future finish":   {func(r map[string]any) { r["finished_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339) }, http.StatusBadRequest, "in the future"},
		"no clone":        {func(r map[string]any) { delete(r, "clone") }, http.StatusBadRequest, "clone is required"},
	}
	for name, c := range cases {
		f := &drillFakes{verdict: goodVerdict()}
		req := goodDrillRequest()
		c.edit(req)
		rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", req)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s, want %d containing %q", name, rec.Code, rec.Body.String(), c.code, c.want)
		}
		if len(f.toolArgs) != 0 || len(f.posted) != 0 {
			t.Errorf("%s: the agent was called %d times and %d records posted", name, len(f.toolArgs), len(f.posted))
		}
	}
}

func TestRecordDrill_ToolFailuresRecordNothing(t *testing.T) {
	// A tool answer without a verdict (an unusable verify_sql, an unregistered database).
	f := &drillFakes{toolText: "verify_sql must be a single statement (no ';')"}
	rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", goodDrillRequest())
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "single statement") || len(f.posted) != 0 {
		t.Errorf("no verdict: %d %s, posted %d", rec.Code, rec.Body.String(), len(f.posted))
	}
	// The agent itself fails.
	f = &drillFakes{toolStatus: http.StatusInternalServerError}
	rec = drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", goodDrillRequest())
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "could not verify the clone") || len(f.posted) != 0 {
		t.Errorf("agent down: %d %s", rec.Code, rec.Body.String())
	}
	// No database agent registered at all.
	f = &drillFakes{verdict: goodVerdict()}
	gw := f.gateway(t)
	gw.agents = nil
	if rec = drillCall(t, gw, http.MethodPost, "/api/v1/drills", goodDrillRequest()); rec.Code != http.StatusBadGateway {
		t.Errorf("no agent: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordDrill_AuditdAnswersAreSurfaced(t *testing.T) {
	f := &drillFakes{verdict: goodVerdict(), auditCode: http.StatusBadRequest, auditBody: "supersedes drl_x: drill not found"}
	rec := drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", goodDrillRequest())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "drill not found") {
		t.Errorf("a refusal from auditd: %d %s", rec.Code, rec.Body.String())
	}
	f = &drillFakes{verdict: goodVerdict(), auditCode: http.StatusInternalServerError, auditBody: "db down"}
	rec = drillCall(t, f.gateway(t), http.MethodPost, "/api/v1/drills", goodDrillRequest())
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "did not record the drill") {
		t.Errorf("auditd down: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordDrill_NeedsAuditAndInfra(t *testing.T) {
	f := &drillFakes{verdict: goodVerdict()}
	gw := f.gateway(t)
	gw.auditURL = ""
	if rec := drillCall(t, gw, http.MethodPost, "/api/v1/drills", goodDrillRequest()); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no audit service: %d", rec.Code)
	}
	gw = f.gateway(t)
	gw.infra = nil
	if rec := drillCall(t, gw, http.MethodPost, "/api/v1/drills", goodDrillRequest()); rec.Code != http.StatusNotFound {
		t.Errorf("no infra config: %d", rec.Code)
	}
}

// auditedGateway is a drill gateway whose own audit trail is a real store, so a
// test can read back what a failed attempt left in it.
func auditedGateway(t *testing.T, f *drillFakes) (*Gateway, *audit.Store) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "gw.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	gw := f.gateway(t)
	gw.auditor = audit.NewGatewayAuditor(store)
	return gw, store
}

func attemptEvents(t *testing.T, store *audit.Store) []audit.Event {
	t.Helper()
	all, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeGatewayRequest})
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Event
	for _, e := range all {
		if e.Tool != nil && e.Tool.Name == "record_restore_drill" {
			out = append(out, e)
		}
	}
	return out
}

func TestRecordDrill_FailedAttemptsAreAudited(t *testing.T) {
	long := strings.Repeat("select 1 -- ", 40)
	cases := map[string]struct {
		fakes *drillFakes
		edit  func(map[string]any)
		code  int
		want  string
	}{
		"rejected request":      {&drillFakes{verdict: goodVerdict()}, func(r map[string]any) { r["method"] = "dump" }, http.StatusBadRequest, "not one of"},
		"unregistered clone":    {&drillFakes{verdict: goodVerdict()}, func(r map[string]any) { r["clone"] = "new-clone" }, http.StatusNotFound, "has to be registered"},
		"clone is the source":   {&drillFakes{verdict: goodVerdict()}, func(r map[string]any) { r["clone"] = "prod-orders" }, http.StatusBadRequest, "cannot be the source"},
		"verification failed":   {&drillFakes{toolStatus: http.StatusInternalServerError}, func(r map[string]any) {}, http.StatusBadGateway, "could not verify the clone"},
		"no verdict":            {&drillFakes{toolText: "verify_sql must be a single statement (no ';')"}, func(r map[string]any) { r["verify_sql"] = long }, http.StatusUnprocessableEntity, "single statement"},
		"audit service refused": {&drillFakes{verdict: goodVerdict(), auditCode: http.StatusInternalServerError, auditBody: "db down"}, func(r map[string]any) {}, http.StatusBadGateway, "did not record the drill"},
	}
	for name, c := range cases {
		gw, store := auditedGateway(t, c.fakes)
		req := goodDrillRequest()
		c.edit(req)
		rec := drillCall(t, gw, http.MethodPost, "/api/v1/drills", req)
		if rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), c.code)
		}
		evs := attemptEvents(t, store)
		if len(evs) != 1 {
			t.Errorf("%s: %d audit events for the attempt, want 1", name, len(evs))
			continue
		}
		e := evs[0]
		if e.Tool.Error == "" || !strings.Contains(e.Tool.Error, c.want) {
			t.Errorf("%s: the event does not say why: %q", name, e.Tool.Error)
		}
		if e.ActionClass != audit.ActionRead {
			t.Errorf("%s: action class %q: a failed attempt must not be classified as a mutation", name, e.ActionClass)
		}
		if e.Tool.Parameters["source"] != "prod-orders" || e.Tool.Parameters["ticket"] != "DRILL-7" {
			t.Errorf("%s: the event lacks who the drill was for: %v", name, e.Tool.Parameters)
		}
		if _, leaked := e.Tool.Parameters["note"]; leaked {
			t.Errorf("%s: the free-text note is in the audit event", name)
		}
		if sql, _ := e.Tool.Parameters["verify_sql"].(string); len(sql) > 210 {
			t.Errorf("%s: verify_sql is not bounded: %d characters", name, len(sql))
		}
	}
}

func TestRecordDrill_ASuccessfulRecordLeavesNoFailureEvent(t *testing.T) {
	gw, store := auditedGateway(t, &drillFakes{verdict: goodVerdict()})
	if rec := drillCall(t, gw, http.MethodPost, "/api/v1/drills", goodDrillRequest()); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if evs := attemptEvents(t, store); len(evs) != 0 {
		t.Errorf("a recorded drill produced %d failed-attempt events: its own restore_drill event is its record", len(evs))
	}
}

// A refused clone is a recorded drill, not a failed attempt.
func TestRecordDrill_ARefusedCloneIsNotAFailedAttempt(t *testing.T) {
	v := goodVerdict()
	v.Status = audit.DrillRefused
	gw, store := auditedGateway(t, &drillFakes{verdict: v})
	if rec := drillCall(t, gw, http.MethodPost, "/api/v1/drills", goodDrillRequest()); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if evs := attemptEvents(t, store); len(evs) != 0 {
		t.Errorf("%d failed-attempt events for a drill that was recorded as refused", len(evs))
	}
}

func TestRecordDrill_ConfigurationFailuresAreAuditedToo(t *testing.T) {
	for name, edit := range map[string]func(*Gateway){
		"no audit service": func(g *Gateway) { g.auditURL = "" },
		"no infra config":  func(g *Gateway) { g.infra = nil },
	} {
		gw, store := auditedGateway(t, &drillFakes{verdict: goodVerdict()})
		edit(gw)
		drillCall(t, gw, http.MethodPost, "/api/v1/drills", goodDrillRequest())
		if evs := attemptEvents(t, store); len(evs) != 1 {
			t.Errorf("%s: %d events, want 1", name, len(evs))
		}
	}
}

func TestValidateDrillRequest_Duration(t *testing.T) {
	now := time.Now().UTC()
	base := func() drillRequest {
		return drillRequest{Source: "a", Clone: "b", Method: "snapshot"}
	}
	r := base()
	r.DurationSeconds = 120
	if err := validateDrillRequest(&r, now); err != nil || r.DurationSeconds != 120 {
		t.Errorf("a stated duration: %v %d", err, r.DurationSeconds)
	}
	r = base()
	r.StartedAt, r.FinishedAt, r.DurationSeconds = now.Add(-10*time.Minute), now.Add(-5*time.Minute), 9999
	if err := validateDrillRequest(&r, now); err != nil || r.DurationSeconds != 300 {
		t.Errorf("the two times win over a stated duration: %v %d", err, r.DurationSeconds)
	}
	r = base()
	r.StartedAt, r.DurationSeconds = now.Add(-10*time.Minute), 60
	if err := validateDrillRequest(&r, now); err != nil || r.FinishedAt.IsZero() {
		t.Errorf("started + duration gives the finish: %v %v", err, r.FinishedAt)
	}
	r = base()
	r.StartedAt, r.FinishedAt = now.Add(-time.Minute), now.Add(-time.Hour)
	if err := validateDrillRequest(&r, now); err == nil {
		t.Error("finished before started must be rejected")
	}
	r = base()
	r.Note = strings.Repeat("x", drillNoteMax+1)
	if err := validateDrillRequest(&r, now); err == nil {
		t.Error("an overlong note must be rejected")
	}
}

func TestDrillInfraDigest_HoldsNoSecretAndNoticesChanges(t *testing.T) {
	cfg := drillInfra()
	a, b := cfg.DBServers["prod-orders"], cfg.DBServers["orders-clone"]
	d1 := drillInfraDigest(a, b)
	if d1 != drillInfraDigest(a, b) {
		t.Error("the digest is not stable")
	}
	b2 := b
	b2.ConnectionString = "host=elsewhere.example.com port=5432 dbname=o user=u"
	if drillInfraDigest(a, b2) == d1 {
		t.Error("repointing the clone did not change the digest")
	}
	// A password in the connection string or the environment must not change it.
	a2 := a
	a2.ConnectionString = strings.Replace(a.ConnectionString, "s3cret", "different", 1)
	if drillInfraDigest(a2, b) != d1 {
		t.Error("the digest depends on a password")
	}
	if strings.Contains(d1, "s3cret") {
		t.Error("secret in the digest")
	}
}

func TestDrillHostType(t *testing.T) {
	for want, d := range map[string]infra.DBServer{
		"k8s":          {K8sCluster: "c"},
		"vm":           {VMName: "v"},
		"vm-container": {VMName: "v", ContainerName: "c"},
		"container":    {ContainerName: "c"},
		"systemd":      {SystemdUnit: "postgresql"},
		"host":         {},
	} {
		if got := drillHostType(d); got != want {
			t.Errorf("%+v: %q, want %q", d, got, want)
		}
	}
}

func TestListDrills_ResolvesTheSourceAndForwardsFilters(t *testing.T) {
	f := &drillFakes{}
	gw := f.gateway(t)
	rec := drillCall(t, gw, http.MethodGet, "/api/v1/drills?source=orders&status=verified&current=true&limit=5&since=2026-10-01T00:00:00Z", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	q := f.listQuery[0]
	if q.Get("source") != "prod-orders" || q.Get("status") != "verified" || q.Get("current") != "true" || q.Get("limit") != "5" || q.Get("since") == "" {
		t.Errorf("auditd was asked %v", q)
	}
	// A source that is not in the config is passed as given: its records, if any, are still found.
	drillCall(t, gw, http.MethodGet, "/api/v1/drills?source=retired-db", nil)
	if f.listQuery[1].Get("source") != "retired-db" {
		t.Errorf("unresolvable source: %v", f.listQuery[1])
	}
	if rec := drillCall(t, gw, http.MethodGet, "/api/v1/drills/drl_missing", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown drill: %d", rec.Code)
	}
}

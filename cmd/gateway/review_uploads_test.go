package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/fleet"
	"helpdesk/internal/infra"
	"helpdesk/internal/toolregistry"
)

// ── fleet review ──────────────────────────────────────────────────────────────

func reviewRegistry() *toolregistry.Registry {
	return toolregistry.New([]toolregistry.ToolEntry{
		{Name: "run_vacuum", Agent: "database", ActionClass: "write", FleetEligible: true, AgentVersion: "1.2.0", SchemaFingerprint: "fp_vacuum_1"},
		{Name: "get_status_summary", Agent: "database", ActionClass: "read", FleetEligible: true, AgentVersion: "1.2.0", SchemaFingerprint: "fp_status_1"},
	})
}

func postReview(t *testing.T, g *Gateway, body any) (*httptest.ResponseRecorder, FleetReviewResponse) {
	t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case string:
		rdr = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		rdr = bytes.NewReader(data)
	}
	rec := httptest.NewRecorder()
	g.handleFleetReview(rec, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/review", rdr))
	var out FleetReviewResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v\n%s", err, rec.Body.String())
		}
	}
	return rec, out
}

func reviewJob(tools ...string) fleet.JobDef {
	def := fleet.JobDef{Name: "j"}
	for _, tool := range tools {
		def.Change.Steps = append(def.Change.Steps, fleet.Step{Agent: "database", Tool: tool})
	}
	return def
}

func issueCodes(out FleetReviewResponse) []string {
	var codes []string
	for _, i := range out.Issues {
		codes = append(codes, i.Severity+":"+i.Code)
	}
	return codes
}

func hasIssue(out FleetReviewResponse, severity, code string) *FleetReviewIssue {
	for i := range out.Issues {
		if out.Issues[i].Severity == severity && out.Issues[i].Code == code {
			return &out.Issues[i]
		}
	}
	return nil
}

func TestFleetReview_Refusals(t *testing.T) {
	g := &Gateway{toolRegistry: reviewRegistry()}
	if rec, _ := postReview(t, &Gateway{}, FleetReviewRequest{JobDef: reviewJob("run_vacuum")}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no registry: %d", rec.Code)
	}
	if rec, _ := postReview(t, g, "{nope"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Errorf("not JSON: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := postReview(t, g, FleetReviewRequest{JobDef: fleet.JobDef{Name: "empty"}}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no steps") {
		t.Errorf("no steps: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetReview_AnUnknownToolIsAnError(t *testing.T) {
	_, out := postReview(t, &Gateway{toolRegistry: reviewRegistry()}, FleetReviewRequest{JobDef: reviewJob("run_vacuum", "drop_everything")})
	if out.OK {
		t.Error("a job with an unknown tool is OK")
	}
	if i := hasIssue(out, "error", "unknown_tool"); i == nil || !strings.Contains(i.Message, `"drop_everything"`) {
		t.Errorf("issues = %v", issueCodes(out))
	}
}

// With no snapshots, drift cannot be checked, and the review says so instead of passing.
func TestFleetReview_NoSnapshotsIsAWarning(t *testing.T) {
	_, out := postReview(t, &Gateway{toolRegistry: reviewRegistry()}, FleetReviewRequest{JobDef: reviewJob("run_vacuum")})
	if out.OK || hasIssue(out, "warning", "no_snapshots") == nil || out.SnapshotAge != "" {
		t.Errorf("ok=%v issues=%v age=%q", out.OK, issueCodes(out), out.SnapshotAge)
	}
}

func TestFleetReview_SchemaDriftAndVersionChanges(t *testing.T) {
	captured := time.Now().UTC().Add(-3 * time.Hour)
	snap := func(version, fp string) fleet.ToolSnapshot {
		return fleet.ToolSnapshot{AgentVersion: version, SchemaFingerprint: fp, CapturedAt: captured}
	}
	g := &Gateway{toolRegistry: reviewRegistry()}
	review := func(snaps map[string]fleet.ToolSnapshot) FleetReviewResponse {
		def := reviewJob("run_vacuum")
		def.ToolSnapshots = snaps
		_, out := postReview(t, g, FleetReviewRequest{JobDef: def})
		return out
	}

	// Everything as planned: clean, and the oldest snapshot's age is reported.
	clean := review(map[string]fleet.ToolSnapshot{"run_vacuum": snap("1.2.0", "fp_vacuum_1")})
	if !clean.OK || len(clean.Issues) != 0 || !strings.HasPrefix(clean.SnapshotAge, "3h") {
		t.Errorf("clean: ok=%v issues=%v age=%q", clean.OK, issueCodes(clean), clean.SnapshotAge)
	}

	// The schema changed since the plan: an error that names both fingerprints and versions.
	drift := review(map[string]fleet.ToolSnapshot{"run_vacuum": snap("1.1.0", "fp_vacuum_OLD")})
	i := hasIssue(drift, "error", "schema_drift")
	if drift.OK || i == nil || !strings.Contains(i.Message, "fp_vacuum_OLD") || !strings.Contains(i.Message, "fp_vacuum_1") ||
		!strings.Contains(i.Message, "planned version=1.1.0, current=1.2.0") {
		t.Errorf("drift: ok=%v issues=%v %+v", drift.OK, issueCodes(drift), i)
	}
	if hasIssue(drift, "warning", "version_changed") != nil {
		t.Error("a drifted schema was also reported as only a version change")
	}

	// Same schema, new agent version: a warning only.
	ver := review(map[string]fleet.ToolSnapshot{"run_vacuum": snap("1.0.0", "fp_vacuum_1")})
	if ver.OK || hasIssue(ver, "warning", "version_changed") == nil || hasIssue(ver, "error", "schema_drift") != nil {
		t.Errorf("version change: %v", issueCodes(ver))
	}

	// A tool in the snapshots that the registry no longer has.
	gone := review(map[string]fleet.ToolSnapshot{"run_vacuum": snap("1.2.0", "fp_vacuum_1"), "retired_tool": snap("0.1.0", "fp_x")})
	if i := hasIssue(gone, "warning", "tool_removed"); gone.OK || i == nil || !strings.Contains(i.Message, `"retired_tool"`) {
		t.Errorf("removed: %v", issueCodes(gone))
	}

	// A snapshot with no fingerprint (or no version) cannot be compared, and is not a finding.
	blank := review(map[string]fleet.ToolSnapshot{"run_vacuum": {CapturedAt: captured}})
	if !blank.OK || len(blank.Issues) != 0 {
		t.Errorf("a snapshot with nothing to compare: %v", issueCodes(blank))
	}
	// The oldest of several snapshots sets the age.
	older := review(map[string]fleet.ToolSnapshot{
		"run_vacuum":         snap("1.2.0", "fp_vacuum_1"),
		"get_status_summary": {AgentVersion: "1.2.0", SchemaFingerprint: "fp_status_1", CapturedAt: time.Now().UTC().Add(-50 * time.Hour)},
	})
	if !strings.HasPrefix(older.SnapshotAge, "50h") {
		t.Errorf("age = %q, want the oldest (50h)", older.SnapshotAge)
	}
}

func TestFleetReview_RestrictedAndNewServers(t *testing.T) {
	cfg := &infra.Config{DBServers: map[string]infra.DBServer{
		"prod-ledger": {ConnectionString: "host=a", Sensitivity: []string{"pii"}, Tags: []string{"prod"}},
		"prod-orders": {ConnectionString: "host=b", Tags: []string{"prod"}},
		"prod-search": {ConnectionString: "host=c", Tags: []string{"prod"}},
		"dev-1":       {ConnectionString: "host=d", Tags: []string{"dev"}},
	}}
	g := &Gateway{toolRegistry: reviewRegistry(), infra: cfg}
	def := reviewJob("run_vacuum")
	def.ToolSnapshots = map[string]fleet.ToolSnapshot{"run_vacuum": {AgentVersion: "1.2.0", SchemaFingerprint: "fp_vacuum_1", CapturedAt: time.Now()}}
	def.Targets = fleet.Targets{Tags: []string{"prod"}}
	// Planned when only orders existed among the prod servers.
	def.PlanServers = []string{"prod-orders"}

	rec, out := postReview(t, g, FleetReviewRequest{JobDef: def})
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if i := hasIssue(out, "warning", "restricted_server"); i == nil || !strings.Contains(i.Message, `"prod-ledger"`) {
		t.Errorf("issues = %v", issueCodes(out))
	}
	if strings.Join(out.NewServers, ",") != "prod-ledger,prod-search" {
		t.Errorf("new servers = %v, want prod-ledger,prod-search (sorted)", out.NewServers)
	}
	if i := hasIssue(out, "info", "new_servers"); i == nil || !strings.Contains(i.Message, "2 server(s) added since plan") {
		t.Errorf("issues = %v", issueCodes(out))
	}
	if out.OK {
		t.Error("a job that targets a restricted server is OK")
	}

	// New servers alone are information, not a reason to fail the review.
	def.Targets = fleet.Targets{Names: []string{"prod-orders", "prod-search"}}
	_, out = postReview(t, g, FleetReviewRequest{JobDef: def})
	if !out.OK || hasIssue(out, "info", "new_servers") == nil || strings.Join(out.NewServers, ",") != "prod-search" {
		t.Errorf("ok=%v issues=%v new=%v: an added server is information only", out.OK, issueCodes(out), out.NewServers)
	}
	// With no PlanServers there is nothing to compare, so nothing is claimed.
	def.PlanServers = nil
	_, out = postReview(t, g, FleetReviewRequest{JobDef: def})
	if len(out.NewServers) != 0 || hasIssue(out, "info", "new_servers") != nil {
		t.Errorf("new servers claimed with no plan to compare: %v", out.NewServers)
	}
}

// ── upload proxies ────────────────────────────────────────────────────────────

type uploadAuditd struct {
	method, path, ctype, auth, user, body string
}

func newUploadAuditd(t *testing.T, status int, ctype, disposition, reply string) (*uploadAuditd, string) {
	t.Helper()
	got := &uploadAuditd{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = uploadAuditd{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.Header.Get("X-User"), string(b)}
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		w.WriteHeader(status)
		io.WriteString(w, reply) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return got, srv.URL
}

func TestUploadCreate_ForwardsTheMultipartBodyAndCredentials(t *testing.T) {
	got, url := newUploadAuditd(t, http.StatusCreated, "application/json", "", `{"upload_id":"upl_1"}`)
	g := &Gateway{auditURL: url + "/", auditAPIKey: "svc-key"}
	const ctype = "multipart/form-data; boundary=XyZ123"
	body := "--XyZ123\r\nContent-Disposition: form-data; name=\"file\"; filename=\"pg.log\"\r\n\r\nLOG LINE\r\n--XyZ123--\r\n"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/uploads", strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("X-User", "alice")
	rec := httptest.NewRecorder()
	g.handleUploadCreate(rec, req)

	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "upl_1") || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("response: %d %s", rec.Code, rec.Body.String())
	}
	if got.method != http.MethodPost || got.path != "/v1/uploads" || got.ctype != ctype || got.auth != "Bearer svc-key" || got.user != "alice" || got.body != body {
		t.Errorf("auditd was sent %+v", *got)
	}
}

// Whatever auditd answers, including a refusal, goes back to the caller as it came.
func TestUploadCreate_PassesAuditdsStatusThrough(t *testing.T) {
	_, url := newUploadAuditd(t, http.StatusRequestEntityTooLarge, "application/json", "", `{"error":"too large"}`)
	rec := httptest.NewRecorder()
	(&Gateway{auditURL: url}).handleUploadCreate(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("x")))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestUploadHandlers_FailuresAreNamed(t *testing.T) {
	type call func(g *Gateway) *httptest.ResponseRecorder
	create := func(g *Gateway) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		g.handleUploadCreate(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("x")))
		return rec
	}
	content := func(g *Gateway) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.SetPathValue("uploadID", "upl_1")
		rec := httptest.NewRecorder()
		g.handleUploadGetContent(rec, req)
		return rec
	}
	for name, c := range map[string]call{"create": create, "content": content} {
		if rec := c(&Gateway{}); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "auditd URL not configured") {
			t.Errorf("%s with no auditd: %d %s", name, rec.Code, rec.Body.String())
		}
		if rec := c(&Gateway{auditURL: "http://127.0.0.1:1"}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "auditd request failed") {
			t.Errorf("%s with auditd down: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// The content route keeps auditd's own content headers: the log is text, not JSON.
func TestUploadGetContent_KeepsTheContentHeaders(t *testing.T) {
	got, url := newUploadAuditd(t, http.StatusOK, "text/plain; charset=utf-8", `attachment; filename="pg.log"`, "LOG LINE 1\nLOG LINE 2\n")
	g := &Gateway{auditURL: url, auditAPIKey: "svc-key"}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/uploads/upl_9/content", nil)
	req.SetPathValue("uploadID", "upl_9")
	rec := httptest.NewRecorder()
	g.handleUploadGetContent(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "LOG LINE 1\nLOG LINE 2\n" {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Header().Get("Content-Disposition") != `attachment; filename="pg.log"` {
		t.Errorf("headers = %v", rec.Header())
	}
	if got.method != http.MethodGet || got.path != "/v1/uploads/upl_9/content" || got.auth != "Bearer svc-key" {
		t.Errorf("auditd was sent %+v", *got)
	}
}

func TestUploadGet_ProxiesTheMetadata(t *testing.T) {
	got, url := newUploadAuditd(t, http.StatusOK, "application/json", "", `{"upload_id":"upl_9","filename":"pg.log"}`)
	g := &Gateway{auditURL: url}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/uploads/upl_9", nil)
	req.SetPathValue("uploadID", "upl_9")
	rec := httptest.NewRecorder()
	g.handleUploadGet(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pg.log") || got.path != "/v1/uploads/upl_9" {
		t.Errorf("%d %s, auditd saw %q", rec.Code, rec.Body.String(), got.path)
	}
}

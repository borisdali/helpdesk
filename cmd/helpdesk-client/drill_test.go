package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/client"
)

// captureStdout returns what fn printed to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestDrillRecordBody(t *testing.T) {
	o := drillOpts{
		Source: " prod-orders ", Clone: "orders-clone", Method: "pgbackrest",
		Started: "2026-11-19T02:00:00+02:00", Finished: "2026-11-19T02:01:35+02:00",
		Duration: "5m", BackupLabel: "20261101-020000F", VerifySQL: "select true", Ticket: " DRILL-7 ", Note: "Q4",
		Supersedes: "drl_old00001",
	}
	b, err := o.recordBody()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(b, &got) //nolint:errcheck
	want := map[string]any{
		"source": "prod-orders", "clone": "orders-clone", "method": "pgbackrest",
		"started_at": "2026-11-19T00:00:00Z", "finished_at": "2026-11-19T00:01:35Z", // normalised to UTC
		"duration_seconds": float64(300), "backup_label": "20261101-020000F", "verify_sql": "select true",
		"ticket": "DRILL-7", "note": "Q4", "supersedes": "drl_old00001",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// Nothing the operator did not give is sent.
	b, _ = drillOpts{Source: "a", Clone: "b", Method: "snapshot"}.recordBody()
	got = nil
	json.Unmarshal(b, &got) //nolint:errcheck
	if len(got) != 3 {
		t.Errorf("a minimal drill sent %v", got)
	}
}

func TestDrillRecordBody_Refusals(t *testing.T) {
	ok := drillOpts{Source: "a", Clone: "b", Method: "other"}
	cases := map[string]struct {
		edit func(*drillOpts)
		want string
	}{
		"no source":     {func(o *drillOpts) { o.Source = " " }, "--drill-source"},
		"no clone":      {func(o *drillOpts) { o.Clone = "" }, "--drill-clone"},
		"no method":     {func(o *drillOpts) { o.Method = "" }, "--drill-method"},
		"bad started":   {func(o *drillOpts) { o.Started = "yesterday" }, "--drill-started"},
		"bad finished":  {func(o *drillOpts) { o.Finished = "2026-11-19 02:00" }, "--drill-finished"},
		"bad duration":  {func(o *drillOpts) { o.Duration = "a while" }, "--drill-duration"},
		"zero duration": {func(o *drillOpts) { o.Duration = "0s" }, "--drill-duration"},
		"negative":      {func(o *drillOpts) { o.Duration = "-5m" }, "--drill-duration"},
	}
	for name, c := range cases {
		o := ok
		c.edit(&o)
		if _, err := o.recordBody(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %s", name, err, c.want)
		}
	}
}

func drillGateway(t *testing.T, status int, body any, seen *http.Request, seenBody *[]byte) client.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r.Clone(context.Background())
		}
		if seenBody != nil {
			*seenBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return client.Config{GatewayURL: srv.URL, UserID: "alice", APIKey: "tok"}
}

func recordedResponse(status string) map[string]any {
	return map[string]any{
		"drill": audit.DrillRecord{DrillID: "drl_abc12345", Status: status, Ticket: "DRILL-7",
			DurationSeconds: 95, DurationSource: "reported", RestoreBytes: 98 << 20},
		"verification": "Restore drill of prod-orders on orders-clone: VERIFIED\n  PASS same_system\n",
	}
}

func TestRunDrillRecord_VerifiedPrintsWhatWasRecorded(t *testing.T) {
	var req http.Request
	var sent []byte
	cfg := drillGateway(t, http.StatusCreated, recordedResponse(audit.DrillVerified), &req, &sent)
	out := captureStdout(t, func() {
		if err := runDrillRecord(context.Background(), cfg, drillOpts{Source: "prod-orders", Clone: "orders-clone", Method: "pgbackrest", Duration: "95s"}); err != nil {
			t.Errorf("a verified drill is not an error: %v", err)
		}
	})
	for _, want := range []string{"VERIFIED", "Recorded as drl_abc12345 (verified)", "ticket   DRILL-7", "took     1m 35s (reported)",
		"98.0 MiB measured on the clone", "counts   yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if req.Method != http.MethodPost || req.URL.Path != "/api/v1/drills" || req.Header.Get("X-User") != "alice" || req.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("request %s %s, headers %v", req.Method, req.URL.Path, req.Header)
	}
	if !strings.Contains(string(sent), `"duration_seconds":95`) {
		t.Errorf("body %s", sent)
	}
}

func TestRunDrillRecord_RefusedIsRecordedButFails(t *testing.T) {
	for _, st := range []string{audit.DrillRefused, audit.DrillUnverifiable} {
		cfg := drillGateway(t, http.StatusCreated, recordedResponse(st), nil, nil)
		var err error
		out := captureStdout(t, func() {
			err = runDrillRecord(context.Background(), cfg, drillOpts{Source: "a", Clone: "b", Method: "snapshot"})
		})
		if err == nil || !strings.Contains(err.Error(), "does not count") || !strings.Contains(err.Error(), "drl_abc12345") {
			t.Errorf("%s: exit error %v", st, err)
		}
		if !strings.Contains(out, "counts   no") || !strings.Contains(out, "--drill-supersedes drl_abc12345") {
			t.Errorf("%s: the output must say what to do next:\n%s", st, out)
		}
	}
}

func TestRunDrillRecord_GatewayErrorsAreShownPlainly(t *testing.T) {
	cfg := drillGateway(t, http.StatusNotFound, map[string]string{"error": `the clone "x" is not in the infrastructure configuration`}, nil, nil)
	err := runDrillRecord(context.Background(), cfg, drillOpts{Source: "a", Clone: "x", Method: "snapshot"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "not in the infrastructure configuration") {
		t.Errorf("error %v", err)
	}
	// Nothing is sent when the flags are wrong.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	if err := runDrillRecord(context.Background(), client.Config{GatewayURL: srv.URL}, drillOpts{Source: "a"}); err == nil || hit {
		t.Errorf("an incomplete command reached the gateway (hit=%v, err=%v)", hit, err)
	}
}

func TestRunDrillList_AsksWithTheFiltersAndPrintsATable(t *testing.T) {
	now := time.Now().UTC()
	drills := []audit.DrillRecord{
		{DrillID: "drl_00000001", SourceKey: "prod-orders", CloneKey: "orders-clone", Method: "pgbackrest", Status: audit.DrillVerified,
			FinishedAt: now.Add(-3 * 24 * time.Hour), DurationSeconds: 95, RestoreBytes: 98 << 20, Ticket: "DRILL-7"},
		{DrillID: "drl_00000002", SourceKey: "prod-orders", CloneKey: "orders-clone", Method: "snapshot", Status: audit.DrillVerifiedWithWarnings,
			RecordedAt: now.Add(-2 * time.Hour)},
	}
	var req http.Request
	cfg := drillGateway(t, http.StatusOK, drills, &req, nil)
	out := captureStdout(t, func() {
		if err := runDrillList(context.Background(), cfg, drillOpts{Source: "prod-orders", Status: "verified", Current: true, Limit: 10}); err != nil {
			t.Error(err)
		}
	})
	q := req.URL.Query()
	if q.Get("source") != "prod-orders" || q.Get("status") != "verified" || q.Get("current") != "true" || q.Get("limit") != "10" {
		t.Errorf("query %v", q)
	}
	for _, want := range []string{"DRILL", "drl_00000001", "prod-orders <- orders-clone", "3d ago", "1m 35s", "98.0 MiB", "DRILL-7",
		"drl_00000002", "2h ago", "verified with warnings"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestFormatDrillList_Empty(t *testing.T) {
	if got := formatDrillList(nil, time.Now()); got != "No restore drills recorded.\n" {
		t.Errorf("%q", got)
	}
}

func TestDrillFormatting(t *testing.T) {
	for in, want := range map[int64]string{5: "5s", 65: "1m 05s", 3725: "1h 02m 05s"} {
		if got := drillDur(in); got != want {
			t.Errorf("drillDur(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int64]string{512: "512 B", 2048: "2.0 KiB", 5 << 30: "5.0 GiB"} {
		if got := drillSize(in); got != want {
			t.Errorf("drillSize(%d) = %q, want %q", in, got, want)
		}
	}
}

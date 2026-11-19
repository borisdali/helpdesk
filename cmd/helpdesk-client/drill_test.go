package main

import (
	"context"
	"encoding/json"
	"flag"
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

// A command line with every drill flag reaches the options, each in its own
// field: this is the wiring main.go relies on.
func TestDrillFlags_EachFlagReachesItsOwnField(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	ticket := fs.String("ticket", "", "")
	df := registerDrillFlags(fs, ticket)
	err := fs.Parse([]string{"--drill-record", "--drill-list",
		"--drill-source", "S", "--drill-clone", "C", "--drill-method", "M",
		"--drill-started", "ST", "--drill-finished", "FI", "--drill-duration", "DU",
		"--drill-backup-label", "BL", "--drill-recovery-target", "RT", "--drill-verify-sql", "VS",
		"--drill-note", "NO", "--drill-supersedes", "SU", "--drill-status", "SA", "--drill-current",
		"--ticket", "TK"})
	if err != nil {
		t.Fatal(err)
	}
	want := drillOpts{Source: "S", Clone: "C", Method: "M", Started: "ST", Finished: "FI", Duration: "DU",
		BackupLabel: "BL", RecoveryTarget: "RT", VerifySQL: "VS", Ticket: "TK", Note: "NO", Supersedes: "SU",
		Status: "SA", Current: true}
	if got := df.opts(); got != want {
		t.Errorf("options\n got  %+v\n want %+v", got, want)
	}
	if !df.requested() {
		t.Error("a drill command was given but not seen")
	}
}

func TestDrillFlags_DefaultsAskForNothing(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	ticket := fs.String("ticket", "", "")
	df := registerDrillFlags(fs, ticket)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if df.requested() || df.opts() != (drillOpts{}) {
		t.Errorf("no flags given, got %+v requested=%v", df.opts(), df.requested())
	}
	// Each of the two commands is requested on its own.
	for _, f := range []string{"--drill-record", "--drill-list"} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		df := registerDrillFlags(fs, fs.String("ticket", "", ""))
		fs.Parse([]string{f}) //nolint:errcheck
		if !df.requested() {
			t.Errorf("%s is not seen", f)
		}
	}
}

func TestWrapHanging(t *testing.T) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	long := "  FAIL archive_guard: archive_mode is on and the clone has an archive command that may push into the source's archive; set archive_mode=off on the clone and record the drill again"
	got := wrapHanging(long, 60, 9)
	if flat(got) != flat(long) {
		t.Errorf("wrapping changed the words:\n%s", got)
	}
	for i, l := range strings.Split(got, "\n") {
		if len(l) > 60 {
			t.Errorf("line %d is %d wide: %q", i, len(l), l)
		}
		if i > 0 && !strings.HasPrefix(l, "         ") {
			t.Errorf("continuation line %d does not hang at column 9: %q", i, l)
		}
	}
	if !strings.HasPrefix(got, "  FAIL archive_guard:") {
		t.Errorf("the first line lost its indent: %q", got)
	}
	if short := "  PASS reachable"; wrapHanging(short, 60, 9) != short {
		t.Error("a short line was changed")
	}
	if wrapHanging(long, 0, 9) != long {
		t.Error("width 0 must leave the line alone")
	}
	// A padded label keeps its alignment.
	if got := wrapHanging("  counts   no: "+strings.Repeat("word ", 20), 40, 11); !strings.HasPrefix(got, "  counts   no: word") {
		t.Errorf("padding collapsed: %q", got)
	}
	// A word longer than the room is not split.
	if got := wrapHanging("  FAIL x: "+strings.Repeat("y", 80), 30, 9); !strings.Contains(got, strings.Repeat("y", 80)) {
		t.Errorf("a long word was split: %q", got)
	}
}

func TestFormatDrillRecorded_WrapsUnderTheCheckAndKeepsTheFlagWhole(t *testing.T) {
	r := drillRecordResponse{
		Drill: audit.DrillRecord{DrillID: "drl_abc12345", Status: audit.DrillRefused},
		Verification: "Restore drill of prod on clone: REFUSED\n" +
			"  PASS reachable\n" +
			"  FAIL archive_guard: archive_mode is on and the clone has an archive command that may push into the source's archive; set archive_mode=off on the clone (pgBackRest restore --archive-mode=off) and record the drill again\n",
	}
	out := formatDrillRecorded(r, 80)
	for i, l := range strings.Split(out, "\n") {
		if len(l) > 80 {
			t.Errorf("line %d is %d wide: %q", i, len(l), l)
		}
	}
	if !strings.Contains(out, "\n         ") {
		t.Errorf("the refusal does not hang under the check name:\n%s", out)
	}
	if !strings.Contains(out, "           --drill-supersedes drl_abc12345\n") {
		t.Errorf("the flag was broken across lines:\n%s", out)
	}
	// Unwrapped, the same words.
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if flat(out) != flat(formatDrillRecorded(r, 0)) {
		t.Error("wrapping changed what is said")
	}
}

// The verdict came from a tool reading servers and is printed to the operator's
// terminal: nothing in it may act on that terminal.
func TestFormatDrillRecorded_NothingInTheVerdictCanActOnATerminal(t *testing.T) {
	rlo := string(rune(0x202E)) // built by number: never written as a literal
	r := drillRecordResponse{
		Drill: audit.DrillRecord{DrillID: "drl_abc12345", Status: audit.DrillRefused},
		Verification: "Restore drill of prod on clone: REFUSED\n" +
			"  FAIL archive_mode: could not be read (\"on\x1b[2J\")\n" +
			"  FAIL x: " + rlo + "reversed\x07\r",
	}
	for _, width := range []int{0, 80} {
		out := formatDrillRecorded(r, width)
		if audit.HasUnsafeText(out, true) {
			t.Errorf("width %d: a character that acts on a terminal got through: %q", width, out)
		}
		if !strings.Contains(out, "FAIL archive_mode: could not be read") {
			t.Errorf("width %d: the harmless words were lost:\n%s", width, out)
		}
	}
}

func TestRunDrillList_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	cfg := drillGateway(t, http.StatusBadGateway, map[string]string{"error": "could not reach the audit service"}, nil, nil)
	if err := runDrillList(ctx, cfg, drillOpts{}); err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "could not reach the audit service") {
		t.Errorf("gateway error: %v", err)
	}
	cfg = drillGateway(t, http.StatusOK, "this is not a list", nil, nil)
	if err := runDrillList(ctx, cfg, drillOpts{}); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("unreadable body: %v", err)
	}
	cfg = drillGateway(t, http.StatusOK, []audit.DrillRecord{}, nil, nil)
	if out := captureStdout(t, func() {
		if err := runDrillList(ctx, cfg, drillOpts{}); err != nil {
			t.Error(err)
		}
	}); out != "No restore drills recorded.\n" {
		t.Errorf("empty list printed %q", out)
	}
	if err := runDrillList(ctx, client.Config{GatewayURL: "http://127.0.0.1:1"}, drillOpts{}); err == nil {
		t.Error("an unreachable gateway must be an error")
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

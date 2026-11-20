package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

var drillNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func someDrill(id, status string, ago time.Duration) audit.DrillRecord {
	fin := drillNow.Add(-ago)
	return audit.DrillRecord{
		DrillID: id, SourceKey: "prod-orders", CloneKey: "orders-clone", SourceSystemID: "7001", CloneSystemID: "7001",
		SourceEndpoint: "db1.example.com:5432", CloneEndpoint: "scratch.example.com:5432",
		Method: audit.DrillMethodPgBackRest, Status: status,
		StartedAt: fin.Add(-95 * time.Second), FinishedAt: fin, RecordedAt: fin.Add(5 * time.Minute),
		DurationSeconds: 95, DurationSource: audit.DrillDurationReported, RestoreBytes: 98 << 20,
		RecordedBy: "alice", Ticket: "DRILL-7",
	}
}

// The flags of `vault drills` are registered next to the shared harness flags. A
// name the harness already owns panics at registration ("flag redefined: source")
// and only parsing shows it: the first live run did.
func TestParseDrillsArgs_RegistersNextToTheHarnessFlags(t *testing.T) {
	cfg, o, rest := parseDrillsArgs([]string{
		"--gateway", "http://gw.example.com", "--api-key", "k",
		"--target", "prod-orders", "--status", "refused", "--all", "--days", "7", "--limit", "5",
		"drl_ab12cd34",
	})
	if cfg.GatewayURL != "http://gw.example.com" || cfg.GatewayAPIKey != "k" {
		t.Errorf("shared flags not read: %+v", cfg)
	}
	if o.target != "prod-orders" || o.status != "refused" || !o.all || o.days != 7 || o.limit != 5 {
		t.Errorf("drills flags not read: %+v", o)
	}
	if len(rest) != 1 || rest[0] != "drl_ab12cd34" {
		t.Errorf("the drill id is not left as the argument: %v", rest)
	}
}

func TestParseDrillsArgs_DefaultsAndIDBeforeFlags(t *testing.T) {
	_, o, rest := parseDrillsArgs([]string{"drl_ab12cd34", "--gateway", "http://gw.example.com"})
	if o.limit != 30 || o.target != "" || o.all || o.days != 0 {
		t.Errorf("defaults: %+v", o)
	}
	if len(rest) != 1 || rest[0] != "drl_ab12cd34" {
		t.Errorf("an id given before the flags is lost: %v", rest)
	}
}

func TestFormatDrillTable(t *testing.T) {
	refused := someDrill("drl_bad00002", audit.DrillRefused, 2*24*time.Hour)
	refused.Ticket = ""
	redo := someDrill("drl_ok000003", audit.DrillVerified, 24*time.Hour)
	redo.Supersedes = "drl_bad00002"
	snap := someDrill("drl_snap0004", audit.DrillVerifiedWithWarnings, 20*time.Minute)
	snap.Method, snap.DurationSeconds, snap.RestoreBytes = audit.DrillMethodSnapshot, 0, 0
	out := formatDrillTable([]audit.DrillRecord{snap, redo, refused}, drillNow)
	for _, want := range []string{
		"DRILL", "SOURCE <- CLONE", "STATUS",
		"drl_snap0004", "just now", "snapshot", "verified with warnings",
		"drl_ok000003", "24h ago", "prod-orders <- orders-clone", "1m 35s", "98.0 MiB", "DRILL-7", "(replaces drl_bad00002)",
		"drl_bad00002", "2d ago", "refused",
		"3 drill(s), 1 of them refused or unverifiable and not counted.",
		"faulttest vault drills <drill id>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A drill with no duration or size shows a dash, not a zero.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "drl_snap0004") && (strings.Contains(line, " 0 B") || strings.Contains(line, " 0s")) {
			t.Errorf("zero shown for an unknown value: %q", line)
		}
	}
	if got := formatDrillTable(nil, drillNow); got != "No restore drills recorded.\n" {
		t.Errorf("empty: %q", got)
	}
	// Nothing refused: no sentence about it.
	if got := formatDrillTable([]audit.DrillRecord{redo}, drillNow); strings.Contains(got, "not counted") || !strings.Contains(got, "1 drill(s).") {
		t.Errorf("all verified:\n%s", got)
	}
}

// Keys and statuses have no length limit: a long source or clone key must widen its
// column, not push the ones after it out of line (the first live listing did).
func TestFormatDrillTable_ColumnsStayAlignedWhateverTheKeysLength(t *testing.T) {
	short := someDrill("drl_short001", audit.DrillVerified, time.Hour)
	long := someDrill("drl_long0002", audit.DrillUnverifiable, 2*time.Hour)
	long.SourceKey, long.CloneKey = "pgbackrest-ubuntu-vm", "orders-clone-in-the-disaster-recovery-region"
	long.DurationSeconds, long.RestoreBytes = 0, 0
	out := formatDrillTable([]audit.DrillRecord{short, long}, drillNow)
	lines := strings.Split(out, "\n")
	header := lines[0]
	cols := map[string]int{
		"METHOD": strings.Index(header, "METHOD"), "TOOK": strings.Index(header, "TOOK"),
		"SIZE": strings.Index(header, "SIZE"), "STATUS": strings.Index(header, "STATUS"),
	}
	for _, row := range lines[1:3] {
		// "pgbackrest " with its space: a source key may start with the same word.
		if i := strings.Index(row, "pgbackrest "); i != cols["METHOD"] {
			t.Errorf("method column at %d, header says %d:\n%s", i, cols["METHOD"], out)
		}
		status := "verified"
		if strings.Contains(row, "unverifiable") {
			status = "unverifiable"
		}
		if i := strings.LastIndex(row, status); i != cols["STATUS"] {
			t.Errorf("status column at %d, header says %d:\n%s", i, cols["STATUS"], out)
		}
	}
	if !strings.Contains(out, "orders-clone-in-the-disaster-recovery-region") {
		t.Errorf("the long key was cut:\n%s", out)
	}
	// No line ends in padding.
	for _, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("trailing spaces: %q", l)
		}
	}
}

func TestFormatDrillDetail_Verified(t *testing.T) {
	d := someDrill("drl_ok000003", audit.DrillVerified, 24*time.Hour)
	d.BackupLabel, d.RecoveryTarget, d.Note = "20261101-020000F", "2026-11-18T10:00:00Z", "Q4 drill with the new runbook"
	d.Checks = []audit.DrillCheck{{Name: "same_system", Result: "pass", Detail: "system identifier 7001"}, {Name: "archive_guard", Result: "pass"}}
	d.AuditEventID, d.InfraDigest = "evt_abc12345", "sha256:deadbeef"
	out := formatDrillDetail(d, nil, 100)
	for _, want := range []string{
		"Restore drill drl_ok000003  VERIFIED",
		"Source     prod-orders (db1.example.com:5432, system 7001)",
		"Clone      orders-clone (scratch.example.com:5432, system 7001)",
		"Method     pgbackrest",
		"took 1m 35s (reported)",
		"Size       98.0 MiB measured on the clone",
		"Backup     20261101-020000F", "Target     2026-11-18T10:00:00Z",
		"by alice, ticket DRILL-7",
		"Note       Q4 drill with the new runbook",
		"PASS same_system: system identifier 7001", "PASS archive_guard",
		"This drill is verified: it resets the drill reminder.",
		"Trail: audit event evt_abc12345; infra sha256:deadbeef",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "does not count") {
		t.Errorf("a verified drill says it does not count:\n%s", out)
	}
}

func TestFormatDrillDetail_RefusedAndReplaced(t *testing.T) {
	d := someDrill("drl_bad00002", audit.DrillRefused, 2*24*time.Hour)
	d.Checks = []audit.DrillCheck{
		{Name: "same_system", Result: "pass"},
		{Name: "archive_guard", Result: "fail", Detail: "archive_mode is on and the clone has an archive command that may push into the source's archive; set archive_mode=off on the clone (pgBackRest restore --archive-mode=off) and record the drill again"},
	}
	redo := someDrill("drl_ok000003", audit.DrillVerified, 24*time.Hour)
	redo.Supersedes = "drl_bad00002"
	out := formatDrillDetail(d, []audit.DrillRecord{redo, d}, 80)
	if !strings.Contains(out, "Replaced by drl_ok000003 (verified)") {
		t.Errorf("the replacement is not shown:\n%s", out)
	}
	if !strings.Contains(out, "FAIL archive_guard: archive_mode is on") || !strings.Contains(out, "This drill does not count") ||
		!strings.Contains(out, "--drill-supersedes drl_bad00002") {
		t.Errorf("refusal:\n%s", out)
	}
	// The check text wraps under itself, within the width, and says the same words.
	for i, l := range strings.Split(out, "\n") {
		if len([]rune(l)) > 80 {
			t.Errorf("line %d is %d wide: %q", i, len([]rune(l)), l)
		}
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if flat(formatDrillDetail(d, nil, 0)) != flat(formatDrillDetail(d, nil, 100000)) {
		t.Error("wrapping changed what is said")
	}
	if !strings.Contains(out, "\n           ") {
		t.Errorf("the long check did not wrap under its text:\n%s", out)
	}
	// And the replacement itself says what it replaces.
	if r := formatDrillDetail(redo, nil, 100); !strings.Contains(r, "Replaces    drl_bad00002") && !strings.Contains(r, "Replaces   drl_bad00002") {
		t.Errorf("replacement:\n%s", r)
	}
}

func TestFormatDrillDetail_SparseRecord(t *testing.T) {
	d := audit.DrillRecord{DrillID: "drl_min", SourceKey: "a", CloneKey: "b", Method: "other", Status: audit.DrillUnverifiable,
		RecordedAt: drillNow, Checks: []audit.DrillCheck{{Name: "reachable", Result: "fail", Detail: "the clone could not be reached: connection refused"}}}
	out := formatDrillDetail(d, nil, 100)
	for _, absent := range []string{"Size ", "Backup ", "Target ", "Note ", "Replaces", "Trail:", "took", "system "} {
		if strings.Contains(out, absent) {
			t.Errorf("a field that was not recorded is shown (%q):\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "UNVERIFIABLE") || !strings.Contains(out, "FAIL reachable: the clone could not be reached") || !strings.Contains(out, "does not count") {
		t.Errorf("%s", out)
	}
}

// A record can come from a writer that did not check it, and what is read back is
// printed to whoever reads it: nothing in a drill may act on their terminal.
func TestFormatDrill_NothingRecordedCanActOnATerminal(t *testing.T) {
	rlo := string(rune(0x202E)) // built by number: never written as a literal
	hostile := someDrill("drl_evil\x1b[2J", audit.DrillVerified, time.Hour)
	hostile.SourceKey, hostile.CloneKey = "prod\x1b[31m", "clone"+rlo+"exe"
	hostile.Note = "fine\x1b]0;owned\x07\nsecond line\twith a tab\r overwritten"
	hostile.BackupLabel, hostile.RecoveryTarget = "label\x1b[0m", "target"+rlo
	hostile.RecordedBy, hostile.Ticket, hostile.Supersedes = "alice\x1b[1m", "T-1\x07", "drl_x\x1b[0m"
	hostile.Checks = []audit.DrillCheck{{Name: "same\x1b[0m", Result: "pass\x07", Detail: "ok\x1b[2J then " + rlo + "reversed\r"}}
	hostile.AuditEventID, hostile.InfraDigest = "evt\x1b[0m", "sha256:\x07"
	copyOf := hostile.Note

	for name, out := range map[string]string{
		"table":  formatDrillTable([]audit.DrillRecord{hostile}, drillNow),
		"detail": formatDrillDetail(hostile, []audit.DrillRecord{hostile}, 100),
	} {
		if audit.HasUnsafeText(out, true) {
			t.Errorf("%s still holds a character that acts on a terminal: %q", name, out)
		}
	}
	// The harmless text of the note is kept, and its layout.
	detail := formatDrillDetail(hostile, nil, 100)
	if !strings.Contains(detail, "fine") || !strings.Contains(detail, "second line") || !strings.Contains(detail, "with a tab") {
		t.Errorf("the note's words were lost:\n%s", detail)
	}
	// The caller's record is not changed, and the siblings it was given neither.
	later := []audit.DrillRecord{hostile}
	formatDrillDetail(hostile, later, 100)
	if hostile.Note != copyOf || later[0].Note != copyOf {
		t.Error("formatting changed the record it was given")
	}
	// A hostile status never counts as verified.
	d := someDrill("drl_s", audit.DrillVerified+"\x1b[0m", time.Hour)
	if out := formatDrillTable([]audit.DrillRecord{d}, drillNow); !strings.Contains(out, "1 of them refused or unverifiable and not counted") {
		t.Errorf("a status with an escape sequence counted:\n%s", out)
	}
}

func TestFormatDrillTable_NoFinishTimeUsesWhenItWasRecorded(t *testing.T) {
	d := someDrill("drl_nofinish", audit.DrillVerified, 0)
	d.StartedAt, d.FinishedAt, d.DurationSeconds = time.Time{}, time.Time{}, 0
	d.RecordedAt = drillNow.Add(-5 * 24 * time.Hour)
	out := formatDrillTable([]audit.DrillRecord{d}, drillNow)
	if !strings.Contains(out, "5d ago") {
		t.Errorf("dated by neither the finish nor the record:\n%s", out)
	}
	if detail := formatDrillDetail(d, nil, 100); !strings.Contains(detail, "When       "+d.RecordedAt.UTC().Format("2006-01-02 15:04 UTC")) {
		t.Errorf("detail:\n%s", detail)
	}
}

func TestFormatDrillDetail_LongTextWrapsUnderItsLabel(t *testing.T) {
	d := someDrill("drl_long", audit.DrillVerified, time.Hour)
	d.CloneKey = "orders-clone-in-the-disaster-recovery-region-eu-west-3"
	d.Note = strings.Repeat("quarterly drill with the new runbook and the new clone host; ", 6)
	d.BackupLabel = "20261101-020000F_20261118-020000I"
	out := formatDrillDetail(d, nil, 80)
	for i, l := range strings.Split(out, "\n") {
		if len([]rune(l)) > 80 {
			t.Errorf("line %d is %d wide: %q", i, len([]rune(l)), l)
		}
	}
	var inNote bool
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "  Note"):
			inNote = true
		case inNote && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "             "):
			inNote = false
		case inNote && strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "             "):
			t.Errorf("a continuation of the note does not hang under its text: %q", l)
		}
	}
	if !strings.Contains(out, "\n             ") {
		t.Errorf("the long note did not wrap:\n%s", out)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(flat(out), flat(d.Note)) {
		t.Errorf("wrapping changed the note's words:\n%s", out)
	}
}

// The command end to end against a fake gateway: what it asks, and what it prints.
func TestDrillsReport(t *testing.T) {
	bad := someDrill("drl_bad00002", audit.DrillRefused, 2*24*time.Hour)
	bad.Checks = []audit.DrillCheck{{Name: "archive_guard", Result: "fail", Detail: "would push into the source's archive"}}
	redo := someDrill("drl_ok000003", audit.DrillVerified, 24*time.Hour)
	redo.Supersedes = "drl_bad00002"

	var queries []neturl.Values
	var failSiblings bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/drills/drl_bad00002":
			json.NewEncoder(w).Encode(bad) //nolint:errcheck
		case "/api/v1/drills/drl_nope":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"drill not found"}`)) //nolint:errcheck
		case "/api/v1/drills":
			if failSiblings && r.URL.Query().Get("limit") == "200" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode([]audit.DrillRecord{redo, bad}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// The listing: filters reach the gateway, drills are printed.
	out, err := drillsReport(srv.URL, "k", "", "prod-orders", "refused", false, 30, 5, drillNow, 100)
	if err != nil {
		t.Fatal(err)
	}
	q := queries[0]
	if q.Get("source") != "prod-orders" || q.Get("status") != "refused" || q.Get("current") != "true" || q.Get("limit") != "5" || q.Get("since") == "" {
		t.Errorf("the listing asked %v", q)
	}
	if !strings.Contains(out, "drl_ok000003") || !strings.Contains(out, "drl_bad00002") || !strings.Contains(out, "2 drill(s), 1 of them refused") {
		t.Errorf("listing:\n%s", out)
	}
	// One drill: its replacement is found in the database's drills, which are read
	// including the superseded ones.
	queries = nil
	out, err = drillsReport(srv.URL, "", "drl_bad00002", "ignored", "ignored", false, 0, 30, drillNow, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Replaced by drl_ok000003 (verified)") || !strings.Contains(out, "FAIL archive_guard") {
		t.Errorf("detail:\n%s", out)
	}
	if len(queries) != 2 || queries[1].Get("source") != "prod-orders" || queries[1].Get("current") != "" {
		t.Errorf("the siblings must be read for the drill's own database, superseded ones included: %v", queries)
	}
	// The siblings cannot be read: the drill is still shown, without that line.
	failSiblings = true
	out, err = drillsReport(srv.URL, "", "drl_bad00002", "", "", false, 0, 30, drillNow, 100)
	if err != nil || !strings.Contains(out, "Restore drill drl_bad00002  REFUSED") || strings.Contains(out, "Replaced by") {
		t.Errorf("siblings unreadable: %v\n%s", err, out)
	}
	// An unknown drill says so; an unreachable gateway is an error; an empty list is said.
	if _, err := drillsReport(srv.URL, "", "drl_nope", "", "", false, 0, 30, drillNow, 100); err == nil || !strings.Contains(err.Error(), `no restore drill "drl_nope" is recorded`) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := drillsReport("http://127.0.0.1:1", "", "", "", "", false, 0, 30, drillNow, 100); err == nil {
		t.Error("an unreachable gateway was not an error")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })) //nolint:errcheck
	defer empty.Close()
	if out, err := drillsReport(empty.URL, "", "", "", "", false, 0, 30, drillNow, 100); err != nil || out != "No restore drills recorded.\n" {
		t.Errorf("empty: %v %q", err, out)
	}
}

func TestDrillsQuery(t *testing.T) {
	q := drillsQuery(" prod-orders ", "refused", false, 30, 10, drillNow)
	if q.Get("source") != "prod-orders" || q.Get("status") != "refused" || q.Get("current") != "true" || q.Get("limit") != "10" {
		t.Errorf("query = %v", q)
	}
	since, err := time.Parse(time.RFC3339, q.Get("since"))
	if err != nil || !since.Equal(drillNow.Add(-30*24*time.Hour)) {
		t.Errorf("since = %q (%v)", q.Get("since"), err)
	}
	// --all includes the superseded; no filters means none are sent.
	all := drillsQuery("", "", true, 0, 0, drillNow)
	if len(all) != 0 {
		t.Errorf("no filters, yet %v", all)
	}
}

func drillGatewayServer(t *testing.T, status int, body any, seen *http.Request) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r.Clone(r.Context())
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchDrills_AsksTheGatewayAndSendsTheKey(t *testing.T) {
	var req http.Request
	url := drillGatewayServer(t, http.StatusOK, []audit.DrillRecord{someDrill("drl_a", audit.DrillVerified, time.Hour)}, &req)
	got, err := fetchDrills(url, "tok", neturl.Values{"source": {"prod-orders"}, "current": {"true"}})
	if err != nil || len(got) != 1 || got[0].DrillID != "drl_a" {
		t.Fatalf("%v %v", got, err)
	}
	if req.URL.Path != "/api/v1/drills" || req.URL.Query().Get("source") != "prod-orders" || req.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("%s ?%s %v", req.URL.Path, req.URL.RawQuery, req.Header)
	}
	// A gateway error is an error with its reason, and an unreadable list too.
	url = drillGatewayServer(t, http.StatusBadGateway, map[string]string{"error": "could not reach the audit service"}, nil)
	if _, err := fetchDrills(url, "", nil); err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "could not reach the audit service") {
		t.Errorf("error: %v", err)
	}
	url = drillGatewayServer(t, http.StatusOK, "not a list", nil)
	if _, err := fetchDrills(url, "", nil); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("unreadable: %v", err)
	}
}

func TestFetchDrill_OneByID(t *testing.T) {
	var req http.Request
	d := someDrill("drl_ab12cd34", audit.DrillVerified, time.Hour)
	url := drillGatewayServer(t, http.StatusOK, d, &req)
	got, err := fetchDrill(url, "", "drl_ab12cd34")
	if err != nil || got.DrillID != "drl_ab12cd34" || req.URL.Path != "/api/v1/drills/drl_ab12cd34" {
		t.Fatalf("%v %v %s", got, err, req.URL.Path)
	}
	url = drillGatewayServer(t, http.StatusNotFound, map[string]string{"error": "drill not found"}, nil)
	if _, err := fetchDrill(url, "", "drl_nope"); err == nil || !strings.Contains(err.Error(), `no restore drill "drl_nope" is recorded`) {
		t.Errorf("not found: %v", err)
	}
	// An id with odd characters cannot change the path.
	var odd http.Request
	url = drillGatewayServer(t, http.StatusOK, d, &odd)
	if _, err := fetchDrill(url, "", "../etc/passwd"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(odd.URL.Path, "..") && !strings.HasPrefix(odd.URL.EscapedPath(), "/api/v1/drills/..%2Fetc%2Fpasswd") {
		t.Errorf("the id was not escaped: %s", odd.URL.EscapedPath())
	}
}

func TestDrillSizeAndDuration(t *testing.T) {
	for in, want := range map[int64]string{0: "-", -5: "-", 512: "512 B", 2048: "2.0 KiB", 5 << 30: "5.0 GiB"} {
		if got := drillSizeText(in); got != want {
			t.Errorf("drillSizeText(%d) = %q, want %q", in, got, want)
		}
	}
	if drillDuration(0) != "-" || drillDuration(95) != "1m 35s" {
		t.Errorf("durations: %q %q", drillDuration(0), drillDuration(95))
	}
	for in, want := range map[time.Duration]string{10 * time.Minute: "just now", 5 * time.Hour: "5h ago", 47 * time.Hour: "47h ago", 72 * time.Hour: "3d ago"} {
		if got := drillAgo(in); got != want {
			t.Errorf("drillAgo(%s) = %q, want %q", in, got, want)
		}
	}
}

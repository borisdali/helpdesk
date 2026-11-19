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

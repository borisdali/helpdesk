package main

import (
	"fmt"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

func srcFacts() drillFacts {
	return drillFacts{
		Reached: true, Endpoint: "prod:5432", ServerAddr: "10.0.0.1:5432",
		SystemID: "7001", ArchiveMode: "on",
		ArchiveCommand: "pgbackrest --stanza=main archive-push %p",
		DBBytes:        10 << 30,
	}
}

func cloneFacts() drillFacts {
	return drillFacts{
		Reached: true, Endpoint: "scratch:5432", ServerAddr: "10.0.0.9:5432",
		SystemID: "7001", ArchiveMode: "off", DBBytes: 10 << 30,
	}
}

func resultOf(checks []audit.DrillCheck, name string) audit.DrillCheck {
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	return audit.DrillCheck{Name: name, Result: "MISSING"}
}

func TestAssessDrill(t *testing.T) {
	yes, no := "select true", "select false"
	cases := []struct {
		name   string
		edit   func(src, clone *drillFacts, in *drillInput)
		status string
		check  string // the check expected to carry the verdict
		result string
	}{
		{"clean restored clone", func(s, c *drillFacts, in *drillInput) {}, audit.DrillVerified, "archive_guard", "pass"},
		{"clone unreachable", func(s, c *drillFacts, in *drillInput) { c.Reached, c.Err = false, "connection refused" },
			audit.DrillUnverifiable, "reachable", "fail"},
		{"source unreachable", func(s, c *drillFacts, in *drillInput) { s.Reached, s.Err = false, "timeout" },
			audit.DrillUnverifiable, "reachable", "fail"},
		{"different system", func(s, c *drillFacts, in *drillInput) { c.SystemID = "9999" },
			audit.DrillRefused, "same_system", "fail"},
		{"identifier unreadable", func(s, c *drillFacts, in *drillInput) { c.SystemID = "" },
			audit.DrillRefused, "same_system", "fail"},
		{"clone reached at the source's address", func(s, c *drillFacts, in *drillInput) { c.Endpoint = "PROD:5432" },
			audit.DrillRefused, "not_the_source", "fail"},
		{"both end at the same server", func(s, c *drillFacts, in *drillInput) { c.ServerAddr = s.ServerAddr },
			audit.DrillRefused, "not_the_source", "fail"},
		{"standby streaming", func(s, c *drillFacts, in *drillInput) { c.InRecovery, c.WalReceivers = true, 1 },
			audit.DrillRefused, "not_the_source", "fail"},
		{"primary_conninfo points at the source", func(s, c *drillFacts, in *drillInput) {
			c.PrimaryConninfo = "user=rep host=prod port=5432"
			c.InRecovery = true
		}, audit.DrillRefused, "not_the_source", "fail"},
		{"still in recovery without a receiver", func(s, c *drillFacts, in *drillInput) { c.InRecovery = true },
			audit.DrillVerifiedWithWarnings, "consistent", "warn"},
		{"archiving on with the source's command", func(s, c *drillFacts, in *drillInput) {
			c.ArchiveMode, c.ArchiveCommand = "on", s.ArchiveCommand
		}, audit.DrillRefused, "archive_guard", "fail"},
		{"archiving always with any command", func(s, c *drillFacts, in *drillInput) {
			c.ArchiveMode, c.ArchiveCommand = "always", "cp %p /mnt/x/%f"
		}, audit.DrillRefused, "archive_guard", "fail"},
		{"archive library set", func(s, c *drillFacts, in *drillInput) {
			c.ArchiveMode, c.ArchiveLibrary = "on", "basic_archive"
		}, audit.DrillRefused, "archive_guard", "fail"},
		{"archiving on, a different stanza", func(s, c *drillFacts, in *drillInput) {
			c.ArchiveMode, c.ArchiveCommand = "on", "pgbackrest --stanza=scratch archive-push %p"
		}, audit.DrillVerifiedWithWarnings, "archive_guard", "warn"},
		{"archiving on, same stanza spelled with a space", func(s, c *drillFacts, in *drillInput) {
			c.ArchiveMode, c.ArchiveCommand = "on", "pgbackrest --stanza main archive-push %p"
		}, audit.DrillRefused, "archive_guard", "fail"},
		{"archiving on but no command", func(s, c *drillFacts, in *drillInput) { c.ArchiveMode = "on" },
			audit.DrillVerifiedWithWarnings, "archive_guard", "warn"},
		{"archive_mode unreadable", func(s, c *drillFacts, in *drillInput) { c.ArchiveMode = "" },
			audit.DrillRefused, "archive_guard", "fail"},
		{"size within tolerance", func(s, c *drillFacts, in *drillInput) { c.DBBytes = s.DBBytes + s.DBBytes/6 },
			audit.DrillVerified, "size_class", "pass"},
		{"size too small", func(s, c *drillFacts, in *drillInput) { c.DBBytes = s.DBBytes / 2 },
			audit.DrillVerifiedWithWarnings, "size_class", "warn"},
		{"size too large", func(s, c *drillFacts, in *drillInput) { c.DBBytes = s.DBBytes * 2 },
			audit.DrillVerifiedWithWarnings, "size_class", "warn"},
		{"size unreadable", func(s, c *drillFacts, in *drillInput) { c.DBBytes = 0 },
			audit.DrillVerifiedWithWarnings, "size_class", "warn"},
		{"verify_sql true", func(s, c *drillFacts, in *drillInput) { in.VerifySQL, in.VerifyOK = &yes, true },
			audit.DrillVerified, "verify_sql", "pass"},
		{"verify_sql false", func(s, c *drillFacts, in *drillInput) { in.VerifySQL = &no },
			audit.DrillRefused, "verify_sql", "fail"},
		{"verify_sql errored", func(s, c *drillFacts, in *drillInput) { in.VerifySQL, in.VerifyErr = &no, "relation missing" },
			audit.DrillRefused, "verify_sql", "fail"},
	}
	for _, tc := range cases {
		s, c := srcFacts(), cloneFacts()
		in := drillInput{}
		tc.edit(&s, &c, &in)
		in.Source, in.Clone = s, c
		status, checks := assessDrill(in)
		if status != tc.status {
			t.Errorf("%s: status %q, want %q (%+v)", tc.name, status, tc.status, checks)
		}
		if got := resultOf(checks, tc.check); got.Result != tc.result {
			t.Errorf("%s: check %s = %q, want %q (%s)", tc.name, tc.check, got.Result, tc.result, got.Detail)
		}
	}
}

func TestAssessDrill_RefusalDetailSaysWhatToDo(t *testing.T) {
	s, c := srcFacts(), cloneFacts()
	c.ArchiveMode, c.ArchiveCommand = "on", s.ArchiveCommand
	_, checks := assessDrill(drillInput{Source: s, Clone: c})
	d := resultOf(checks, "archive_guard").Detail
	if !strings.Contains(d, "archive_mode=off") || !strings.Contains(d, "--archive-mode=off") {
		t.Errorf("the refusal must tell the operator the fix: %q", d)
	}
}

func TestStanzaOf(t *testing.T) {
	for cmd, want := range map[string]string{
		"pgbackrest --stanza=main archive-push %p":          "main",
		"pgbackrest --stanza main archive-push %p":          "main",
		"pgbackrest --config=/x --stanza='m1' archive-push": "m1",
		"cp %p /mnt/%f": "",
		"":              "",
	} {
		if got := stanzaOf(cmd); got != want {
			t.Errorf("stanzaOf(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestVerifySQLAllowed(t *testing.T) {
	good := []string{"select count(*) > 0 from t", "  SELECT true", "select (select count(*) from t) = 42"}
	bad := []string{"", "  ", "drop table t", "select 1; drop table t", "delete from t", "with x as (select 1) select true",
		"select " + strings.Repeat("1+", 300) + "1"}
	for _, s := range good {
		if err := verifySQLAllowed(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := verifySQLAllowed(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func factsOutput(f drillFacts) string {
	rec := "f"
	if f.InRecovery {
		rec = "t"
	}
	return fmt.Sprintf(`-[ RECORD 1 ]-----+------------------
system_identifier | %s
in_recovery       | %s
wal_receivers     | %d
primary_conninfo  | %s
archive_mode      | %s
archive_command   | %s
archive_library   | %s
db_bytes          | %d
server_addr       | %s
`, f.SystemID, rec, f.WalReceivers, f.PrimaryConninfo, f.ArchiveMode, f.ArchiveCommand, f.ArchiveLibrary, f.DBBytes, f.ServerAddr)
}

func TestParseDrillFacts(t *testing.T) {
	want := srcFacts()
	got, err := parseDrillFacts(factsOutput(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.SystemID != "7001" || got.ArchiveMode != "on" || got.ArchiveCommand != want.ArchiveCommand ||
		got.DBBytes != 10<<30 || got.InRecovery || got.ServerAddr != "10.0.0.1:5432" {
		t.Errorf("parsed %+v", got)
	}
	if _, err := parseDrillFacts(""); err == nil {
		t.Error("empty output must be an error")
	}
	empty := cloneFacts()
	empty.SystemID = ""
	if _, err := parseDrillFacts(factsOutput(empty)); err == nil {
		t.Error("a server that reports no system identifier must be an error")
	}
}

func TestEndpointOf(t *testing.T) {
	for in, want := range map[string]string{
		"host=db1 port=5433 dbname=x user=u password=secret": "db1:5433",
		"host=db1 dbname=x": "db1:5432",
		"dbname=x":          "localhost:5432",
	} {
		got := endpointOf(in)
		if got != want || strings.Contains(got, "secret") {
			t.Errorf("endpointOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVerifyRestoreDrillTool_EndToEnd(t *testing.T) {
	src, clone := srcFacts(), cloneFacts()
	defer withMockRunnerSequence(psqlResponse{out: factsOutput(src)}, psqlResponse{out: factsOutput(clone)})()
	res, err := verifyRestoreDrillTool(newTestContext(), VerifyRestoreDrillArgs{
		Source: "host=prod port=5432 dbname=app", Clone: "host=scratch port=5432 dbname=app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != audit.DrillVerified || res.SourceSystemID != "7001" || res.CloneSystemID != "7001" ||
		res.SourceEndpoint != "prod:5432" || res.CloneEndpoint != "scratch:5432" {
		t.Errorf("result %+v", res)
	}
	if !strings.Contains(res.Output, "VERIFIED") || !strings.Contains(res.Output, "archive_guard") {
		t.Errorf("output: %q", res.Output)
	}
}

func TestVerifyRestoreDrillTool_RefusesTheSourceAsClone(t *testing.T) {
	// No psql call may happen: the runner would panic on an empty sequence.
	defer withMockRunnerSequence(psqlResponse{err: fmt.Errorf("must not be called")})()
	res, _ := verifyRestoreDrillTool(newTestContext(), VerifyRestoreDrillArgs{
		Source: "host=prod port=5432 dbname=app", Clone: "host=prod port=5432 dbname=other",
	})
	if res.Status != audit.DrillRefused || resultOf(res.Checks, "not_the_source").Result != "fail" {
		t.Errorf("the source as its own clone was not refused: %+v", res)
	}
}

func TestVerifyRestoreDrillTool_UnreachableCloneAndVerifySQL(t *testing.T) {
	src := srcFacts()
	defer withMockRunnerSequence(psqlResponse{out: factsOutput(src)}, psqlResponse{err: fmt.Errorf("connection refused")})()
	res, _ := verifyRestoreDrillTool(newTestContext(), VerifyRestoreDrillArgs{
		Source: "host=prod port=5432", Clone: "host=scratch port=5432", VerifySQL: "select true",
	})
	if res.Status != audit.DrillUnverifiable {
		t.Errorf("status %q, want unverifiable: %+v", res.Status, res)
	}

	// A verify_sql that is not a single SELECT is refused before any server is touched.
	defer withMockRunnerSequence(psqlResponse{err: fmt.Errorf("must not be called")})()
	res, _ = verifyRestoreDrillTool(newTestContext(), VerifyRestoreDrillArgs{
		Source: "host=prod port=5432", Clone: "host=scratch port=5432", VerifySQL: "select 1; drop table t",
	})
	if res.Status != "" || !strings.Contains(res.Output, "single statement") {
		t.Errorf("unsafe verify_sql: %+v", res)
	}
}

func TestVerifyRestoreDrillTool_VerifySQLRunsOnTheClone(t *testing.T) {
	src, clone := srcFacts(), cloneFacts()
	defer withMockRunnerSequence(
		psqlResponse{out: factsOutput(src)},
		psqlResponse{out: factsOutput(clone)},
		psqlResponse{out: "-[ RECORD 1 ]\nverify_ok | t\n"},
	)()
	res, _ := verifyRestoreDrillTool(newTestContext(), VerifyRestoreDrillArgs{
		Source: "host=prod port=5432", Clone: "host=scratch port=5432", VerifySQL: "select count(*) > 0 from t",
	})
	if res.Status != audit.DrillVerified || resultOf(res.Checks, "verify_sql").Result != "pass" {
		t.Errorf("result %+v", res)
	}
}

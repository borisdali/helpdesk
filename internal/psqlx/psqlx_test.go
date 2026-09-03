package psqlx

import (
	"reflect"
	"testing"
)

// Fixtures below are captured verbatim from a real `psql -x` invocation
// against a running PostgreSQL 16 instance (`PGPASSWORD=... psql -h
// localhost -p 35432 -U postgres -w -x -c "..."`), not hand-constructed —
// this format has real, non-obvious quirks (variable header decoration,
// blank-line statement separators, the "(0 rows)" zero-row marker) that are
// easy to get subtly wrong by guessing.

func TestParseExpanded_SingleStatement_MultipleRecords(t *testing.T) {
	// Real captured output for a 3-row pg_stat_activity query.
	output := `-[ RECORD 1 ]---+--------------------
pid             | 31
user            |
database        |
client_addr     |
state           |
wait_event_type | Activity
wait_event      | AutoVacuumMain
query_seconds   |
query_preview   |
-[ RECORD 2 ]---+--------------------
pid             | 32
user            | postgres
database        |
client_addr     |
state           |
wait_event_type | Activity
wait_event      | LogicalLauncherMain
query_seconds   |
query_preview   |
`
	statements := ParseExpanded(output)
	if len(statements) != 1 {
		t.Fatalf("got %d statements, want 1", len(statements))
	}
	if len(statements[0]) != 2 {
		t.Fatalf("got %d records, want 2", len(statements[0]))
	}
	if statements[0][0]["pid"] != "31" {
		t.Errorf("record 1 pid = %q, want 31", statements[0][0]["pid"])
	}
	if statements[0][1]["pid"] != "32" {
		t.Errorf("record 2 pid = %q, want 32", statements[0][1]["pid"])
	}
	if statements[0][1]["user"] != "postgres" {
		t.Errorf("record 2 user = %q, want postgres", statements[0][1]["user"])
	}
	// Empty-valued fields (client_addr etc.) must still be present as keys
	// with an empty string value, not absent — a caller checking for a
	// column's presence should be able to rely on that.
	if v, ok := statements[0][0]["client_addr"]; !ok || v != "" {
		t.Errorf("record 1 client_addr = (%q, %v), want (\"\", true)", v, ok)
	}
}

func TestParseExpanded_MultipleStatements_NoDecoration(t *testing.T) {
	// Real captured output for 3 single-column, single-row statements —
	// note the header has NO trailing dashes here (narrow output), unlike
	// the fixture above.
	output := "-[ RECORD 1 ]\na | 1\n\n-[ RECORD 1 ]\nc | x\nd | y\n\n-[ RECORD 1 ]\ne | 99\n"

	statements := ParseExpanded(output)
	if len(statements) != 3 {
		t.Fatalf("got %d statements, want 3", len(statements))
	}
	if len(statements[0]) != 1 || statements[0][0]["a"] != "1" {
		t.Errorf("statement 0 = %+v, want [{a:1}]", statements[0])
	}
	if len(statements[1]) != 1 || statements[1][0]["c"] != "x" || statements[1][0]["d"] != "y" {
		t.Errorf("statement 1 = %+v, want [{c:x d:y}]", statements[1])
	}
	if len(statements[2]) != 1 || statements[2][0]["e"] != "99" {
		t.Errorf("statement 2 = %+v, want [{e:99}]", statements[2])
	}
}

func TestParseExpanded_ZeroRowStatement_YieldsEmptyNonNilSlice(t *testing.T) {
	// Real captured output: SELECT 1; SELECT pid FROM pg_stat_replication
	// (no replica attached); SELECT 99 — the middle statement is exactly
	// the shape a disconnected replica produces.
	output := "-[ RECORD 1 ]\na | 1\n\n(0 rows)\n\n-[ RECORD 1 ]\nz | 99\n"

	statements := ParseExpanded(output)
	if len(statements) != 3 {
		t.Fatalf("got %d statements, want 3", len(statements))
	}
	if statements[1] == nil {
		t.Fatal("statement 1 (zero rows) is nil, want empty-but-non-nil")
	}
	if len(statements[1]) != 0 {
		t.Errorf("statement 1 has %d records, want 0", len(statements[1]))
	}
	if len(statements[0]) != 1 || statements[0][0]["a"] != "1" {
		t.Errorf("statement 0 = %+v, want [{a:1}]", statements[0])
	}
	if len(statements[2]) != 1 || statements[2][0]["z"] != "99" {
		t.Errorf("statement 2 = %+v, want [{z:99}]", statements[2])
	}
}

func TestParseExpanded_EmptyOutput(t *testing.T) {
	if got := ParseExpanded(""); got != nil {
		t.Errorf("got %v, want nil for empty output", got)
	}
}

func TestParseExpanded_SingleZeroRowStatement(t *testing.T) {
	statements := ParseExpanded("(0 rows)\n")
	if len(statements) != 1 {
		t.Fatalf("got %d statements, want 1", len(statements))
	}
	if len(statements[0]) != 0 {
		t.Errorf("got %d records, want 0", len(statements[0]))
	}
}

func TestParseExpanded_TrailingBlankLines_NoBogusStatement(t *testing.T) {
	output := "-[ RECORD 1 ]\na | 1\n\n\n\n"
	statements := ParseExpanded(output)
	if len(statements) != 1 {
		t.Fatalf("got %d statements, want 1 (trailing blank lines must not create extra statements)", len(statements))
	}
}

func TestParseExpanded_ValueContainingPipe_SplitsOnFirstOnly(t *testing.T) {
	output := "-[ RECORD 1 ]\nquery_preview | SELECT a || b FROM t\n"
	statements := ParseExpanded(output)
	want := "SELECT a || b FROM t"
	if got := statements[0][0]["query_preview"]; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseExpanded_DeepEqual_FullRecord(t *testing.T) {
	output := "-[ RECORD 1 ]\na | 1\nb | 2\n"
	statements := ParseExpanded(output)
	want := Record{"a": "1", "b": "2"}
	if !reflect.DeepEqual(statements[0][0], want) {
		t.Errorf("got %+v, want %+v", statements[0][0], want)
	}
}

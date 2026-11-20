package audit

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

// The drill types cross three programs as JSON: the database agent writes a
// DrillVerification, the gateway reads it and sends a DrillRecord, auditd stores
// it and the clients read it back. A renamed field still compiles but turns
// into an empty value on the far side, so the names are pinned here. Change one
// on purpose and this test says which readers to look at.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestDrillRecord_WireNames(t *testing.T) {
	ts := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	full := DrillRecord{
		DrillID: "d", SourceKey: "s", CloneKey: "c", SourceSystemID: "1", CloneSystemID: "1",
		SourceEndpoint: "h:1", CloneEndpoint: "h:2", SourceHostType: "host", Stanza: "main", InfraDigest: "x",
		Method: "pgbackrest", StartedAt: ts, FinishedAt: ts, DurationSeconds: 1, DurationSource: "reported",
		RestoreBytes: 1, BackupLabel: "l", RecoveryTarget: "t", Status: "verified",
		Checks: []DrillCheck{{Name: "n", Result: "pass", Detail: "d"}}, Supersedes: "p",
		RecordedBy: "a", RecordedAt: ts, Ticket: "T", Note: "n", AuditEventID: "e",
	}
	want := []string{"audit_event_id", "backup_label", "checks", "clone_endpoint", "clone_key", "clone_system_id",
		"drill_id", "duration_seconds", "duration_source", "finished_at", "infra_digest", "method", "note",
		"recorded_at", "recorded_by", "recovery_target", "restore_bytes", "source_endpoint", "source_host_type",
		"source_key", "source_system_id", "stanza", "started_at", "status", "supersedes", "ticket"}
	if got := jsonKeys(t, full); !reflect.DeepEqual(got, want) {
		t.Errorf("DrillRecord wire names changed:\n got  %v\n want %v", got, want)
	}
	if got, want := jsonKeys(t, full.Checks[0]), []string{"detail", "name", "result"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DrillCheck wire names changed: %v", got)
	}
}

func TestDrillVerification_WireNames(t *testing.T) {
	full := DrillVerification{
		Output: "o", Status: "verified", Checks: []DrillCheck{{Name: "n", Result: "pass"}},
		SourceKey: "s", CloneKey: "c", SourceSystemID: "1", CloneSystemID: "1",
		SourceEndpoint: "h:1", CloneEndpoint: "h:2", SourceBytes: 1, CloneBytes: 1,
	}
	want := []string{"checks", "clone_bytes", "clone_endpoint", "clone_key", "clone_system_id", "output",
		"source_bytes", "source_endpoint", "source_key", "source_system_id", "status"}
	if got := jsonKeys(t, full); !reflect.DeepEqual(got, want) {
		t.Errorf("DrillVerification wire names changed:\n got  %v\n want %v", got, want)
	}
}

// What the audit event carries is what the hash covers, under this one key.
func TestEvent_CarriesTheDrillUnderRestoreDrill(t *testing.T) {
	ev := Event{EventType: EventTypeRestoreDrill, RestoreDrill: &DrillRecord{DrillID: "d"}}
	b, _ := json.Marshal(&ev)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["restore_drill"]; !ok || string(EventTypeRestoreDrill) != "restore_drill" {
		t.Errorf("the event lost its restore_drill payload: %s", b)
	}
}

// A drill round-trips through the JSON of a stored event without changing, so
// the hash computed at write time still matches when it is read back.
func TestDrillRecord_SurvivesAnEventRoundTrip(t *testing.T) {
	ts := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	in := DrillRecord{DrillID: "d", SourceKey: "s", CloneKey: "c", Method: "snapshot", Status: "refused",
		RecordedAt: ts, Checks: []DrillCheck{{Name: "archive_guard", Result: "fail", Detail: "x"}}}
	ev := &Event{EventID: "evt_1", Timestamp: ts, EventType: EventTypeRestoreDrill, RestoreDrill: &in}
	want := ComputeEventHash(ev)
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if got := ComputeEventHash(&back); got != want {
		t.Error("the event hashes differently after a JSON round trip: stored drills would fail verification")
	}
}

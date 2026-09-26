package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/evidence"
)

// realPgBackRestInfoJSON is the exact output captured from a real pgBackRest
// 2.59.1 install (2026-09-26): a fresh stanza, one real full backup taken
// against a live target after a genuine `stanza-create` + `backup` run —
// not synthesized from documentation. Used to prove the parser handles real
// output, not just a shape we assumed was right.
const realPgBackRestInfoJSON = `[
    {
        "archive": [
            {
                "database": {"id": 1, "repo-key": 1},
                "id": "16-1",
                "max": "000000010000000000000015",
                "min": "000000010000000000000014"
            }
        ],
        "backup": [
            {
                "archive": {"start": "000000010000000000000015", "stop": "000000010000000000000015"},
                "backrest": {"format": 5, "version": "2.59.1"},
                "database": {"id": 1, "repo-key": 1},
                "error": false,
                "info": {"delta": 31048775, "repository": {"delta": 4123469, "size": 4123469}, "size": 31048775},
                "label": "20260926-040922F",
                "lsn": {"start": "0/15000028", "stop": "0/15000138"},
                "prior": null,
                "reference": null,
                "timestamp": {"start": 1790395762, "stop": 1790395767},
                "type": "full"
            }
        ],
        "cipher": "none",
        "db": [{"id": 1, "repo-key": 1, "system-id": 7689324913302356006, "version": "16"}],
        "name": "main",
        "repo": [{"cipher": "none", "key": 1, "status": {"code": 0, "message": "ok"}}],
        "status": {"code": 0, "lock": {"backup": {"held": false}, "restore": {"held": false}}, "message": "ok"}
    }
]`

func TestParsePgBackRestInfo_RealCapturedSchema(t *testing.T) {
	// maxAge=0 disables staleness checking here — this test is only about
	// correct field extraction from real output, not time-relative logic
	// (the captured timestamp is fixed in the past and would otherwise
	// spuriously "become stale" as real time passes).
	result, err := parsePgBackRestInfo(realPgBackRestInfoJSON, "", 0)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if result.Stanza != "main" {
		t.Errorf("Stanza = %q, want main", result.Stanza)
	}
	if result.StatusCode != 0 || result.StatusMessage != "ok" {
		t.Errorf("StatusCode/StatusMessage = %d/%q, want 0/ok", result.StatusCode, result.StatusMessage)
	}
	if len(result.Backups) != 1 {
		t.Fatalf("len(Backups) = %d, want 1", len(result.Backups))
	}
	b := result.Backups[0]
	if b.Label != "20260926-040922F" || b.Type != "full" || b.Error {
		t.Errorf("Backups[0] = %+v, want label=20260926-040922F type=full error=false", b)
	}
	if result.LastBackupLabel != "20260926-040922F" {
		t.Errorf("LastBackupLabel = %q, want 20260926-040922F", result.LastBackupLabel)
	}
	if result.BackupStale {
		t.Error("BackupStale = true, want false (maxAge=0 disables the check)")
	}
}

// pgBackRestJSONWithBackups builds a synthetic (but schema-accurate)
// info --output=json document with the given backups, so staleness tests
// can use real, controlled, wall-clock-relative timestamps instead of a
// fixed historical capture that would eventually "become stale" on its own.
func pgBackRestJSONWithBackups(stanzaName string, statusCode int, statusMsg string, backups []struct {
	label string
	typ   string
	err   bool
	stop  time.Time
}) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`[{"name":%q,"status":{"code":%d,"message":%q},"backup":[`, stanzaName, statusCode, statusMsg))
	for i, b := range backups {
		if i > 0 {
			sb.WriteString(",")
		}
		start := b.stop.Add(-5 * time.Second)
		fmt.Fprintf(&sb, `{"label":%q,"type":%q,"error":%t,"timestamp":{"start":%d,"stop":%d}}`,
			b.label, b.typ, b.err, start.Unix(), b.stop.Unix())
	}
	sb.WriteString(`]}]`)
	return sb.String()
}

func TestParsePgBackRestInfo_StaleBackup(t *testing.T) {
	backups := []struct {
		label string
		typ   string
		err   bool
		stop  time.Time
	}{
		{label: "old-F", typ: "full", err: false, stop: time.Now().Add(-48 * time.Hour)},
	}
	doc := pgBackRestJSONWithBackups("main", 0, "ok", backups)

	result, err := parsePgBackRestInfo(doc, "main", 25*time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if !result.BackupStale {
		t.Error("BackupStale = false, want true (last backup is 48h old, max age 25h)")
	}
}

func TestParsePgBackRestInfo_FreshBackup(t *testing.T) {
	backups := []struct {
		label string
		typ   string
		err   bool
		stop  time.Time
	}{
		{label: "recent-F", typ: "full", err: false, stop: time.Now().Add(-1 * time.Hour)},
	}
	doc := pgBackRestJSONWithBackups("main", 0, "ok", backups)

	result, err := parsePgBackRestInfo(doc, "main", 25*time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if result.BackupStale {
		t.Error("BackupStale = true, want false (last backup is 1h old, max age 25h)")
	}
}

func TestParsePgBackRestInfo_NeverBackedUp(t *testing.T) {
	doc := pgBackRestJSONWithBackups("main", 0, "ok", nil)

	result, err := parsePgBackRestInfo(doc, "main", 25*time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if result.LastBackupTime != "" {
		t.Errorf("LastBackupTime = %q, want empty (no backup ever succeeded)", result.LastBackupTime)
	}
	// Deliberately false, not true: "never backed up" is a distinct finding
	// from "backed up too long ago" — see GetPgBackRestStatusResult's own
	// doc comment on BackupStale.
	if result.BackupStale {
		t.Error("BackupStale = true, want false when no backup has ever succeeded")
	}
}

func TestParsePgBackRestInfo_MultipleBackups_PicksMostRecentRegardlessOfOrder(t *testing.T) {
	now := time.Now()
	// Deliberately out of chronological order: the most recent backup is
	// listed FIRST, not last — proves the parser compares timestamps
	// explicitly rather than trusting array position.
	backups := []struct {
		label string
		typ   string
		err   bool
		stop  time.Time
	}{
		{label: "newest-I", typ: "incr", err: false, stop: now.Add(-1 * time.Hour)},
		{label: "oldest-F", typ: "full", err: false, stop: now.Add(-72 * time.Hour)},
	}
	doc := pgBackRestJSONWithBackups("main", 0, "ok", backups)

	result, err := parsePgBackRestInfo(doc, "main", 25*time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if result.LastBackupLabel != "newest-I" {
		t.Errorf("LastBackupLabel = %q, want newest-I (the one with the latest timestamp, not the first array element)", result.LastBackupLabel)
	}
	if result.BackupStale {
		t.Error("BackupStale = true, want false (most recent backup is only 1h old)")
	}
}

func TestParsePgBackRestInfo_StanzaNotFound(t *testing.T) {
	doc := pgBackRestJSONWithBackups("main", 0, "ok", nil)
	if _, err := parsePgBackRestInfo(doc, "does-not-exist", time.Hour); err == nil {
		t.Fatal("expected an error for an unknown stanza name, got nil")
	}
}

func TestParsePgBackRestInfo_AmbiguousMultiStanza_RequiresExplicitName(t *testing.T) {
	doc := `[
		{"name":"main","status":{"code":0,"message":"ok"},"backup":[]},
		{"name":"replica","status":{"code":0,"message":"ok"},"backup":[]}
	]`
	_, err := parsePgBackRestInfo(doc, "", time.Hour)
	if err == nil {
		t.Fatal("expected an error when no stanza is specified and more than one is present")
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "replica") {
		t.Errorf("error = %v, want it to name both candidate stanzas", err)
	}
}

func TestParsePgBackRestInfo_NonOkStatus(t *testing.T) {
	doc := pgBackRestJSONWithBackups("main", 2, "missing stanza path", nil)
	result, err := parsePgBackRestInfo(doc, "main", time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if result.StatusCode != 2 || result.StatusMessage != "missing stanza path" {
		t.Errorf("StatusCode/StatusMessage = %d/%q, want 2/missing stanza path", result.StatusCode, result.StatusMessage)
	}
}

func TestGetPgBackRestStatusTool_Success(t *testing.T) {
	defer withMockRunner(realPgBackRestInfoJSON, nil)()

	ctx := mockToolContext{context.Background()}
	result, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{})
	if err != nil {
		t.Fatalf("getPgBackRestStatusTool() error = %v", err)
	}
	if result.Stanza != "main" || result.LastBackupLabel != "20260926-040922F" {
		t.Errorf("got %+v, want stanza=main last_backup_label=20260926-040922F", result)
	}
}

// withPgBackRestEvidenceRules installs a real backup_unhealthy rule for the
// duration of the calling test, mirroring
// agents/database/tools_test.go's withReplicationEvidenceRules.
func withPgBackRestEvidenceRules(t *testing.T) {
	t.Helper()
	orig := pgbackrestEvidenceRules
	pgbackrestEvidenceRules = []evidence.Rule{
		{Tool: "get_pgbackrest_status", Probe: "backup_unhealthy", Operator: "==", Threshold: true, Signal: "pgbackrest_backup_unhealthy", Detail: "pgBackRest backup unhealthy — most recent backup: %s"},
	}
	t.Cleanup(func() { pgbackrestEvidenceRules = orig })
}

func TestGetPgBackRestStatusTool_StaleBackup_RecordsObjectiveEvidence(t *testing.T) {
	withPgBackRestEvidenceRules(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()

	backups := []struct {
		label string
		typ   string
		err   bool
		stop  time.Time
	}{
		{label: "stale-F", typ: "full", err: false, stop: time.Now().Add(-72 * time.Hour)},
	}
	doc := pgBackRestJSONWithBackups("main", 0, "ok", backups)
	defer withMockRunner(doc, nil)()

	ctx := mockToolContext{context.Background()}
	if _, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{Stanza: "main"}); err != nil {
		t.Fatalf("getPgBackRestStatusTool() error = %v", err)
	}

	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeObjectiveEvidence})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 objective_evidence event, got %d", len(events))
	}
	if events[0].ObjectiveEvidence == nil || events[0].ObjectiveEvidence.Signal != "pgbackrest_backup_unhealthy" {
		t.Errorf("ObjectiveEvidence = %+v, want signal=pgbackrest_backup_unhealthy", events[0].ObjectiveEvidence)
	}
}

func TestGetPgBackRestStatusTool_HealthyBackup_NoObjectiveEvidence(t *testing.T) {
	withPgBackRestEvidenceRules(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()

	backups := []struct {
		label string
		typ   string
		err   bool
		stop  time.Time
	}{
		{label: "fresh-F", typ: "full", err: false, stop: time.Now().Add(-1 * time.Hour)},
	}
	doc := pgBackRestJSONWithBackups("main", 0, "ok", backups)
	defer withMockRunner(doc, nil)()

	ctx := mockToolContext{context.Background()}
	if _, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{Stanza: "main"}); err != nil {
		t.Fatalf("getPgBackRestStatusTool() error = %v", err)
	}

	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeObjectiveEvidence})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 objective_evidence events for a healthy, fresh backup, got %d", len(events))
	}
}

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

// realPgBackRestBrokenRepoJSON is the exact output captured live
// (2026-09-27) from the same install after `chmod 000` on the repo path —
// the literal injection db-pgbackrest-repo-unreadable performs. Not a
// synthesized "what we think a permission error looks like" fixture: this
// is what pgBackRest 2.59.1 actually emits, including the surprising
// name="[invalid]" (not the configured stanza name) and the fact that
// status.message ("other") is far less useful than repo[0].status.message.
const realPgBackRestBrokenRepoJSON = `[{"archive":[],"backup":[],"cipher":"none","db":[],"name":"[invalid]","repo":[{"cipher":"none","key":1,"status":{"code":99,"message":"[PathOpenError] unable to list file info for path '/var/lib/pgbackrest/backup': [13] Permission denied"}}],"status":{"code":99,"lock":{"backup":{"held":false},"restore":{"held":false}},"message":"other"}}]`

func TestParsePgBackRestInfo_RealCapturedBrokenRepo(t *testing.T) {
	result, err := parsePgBackRestInfo(realPgBackRestBrokenRepoJSON, "main", 25*time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v, want the single (mismatched-name) stanza to be used", err)
	}
	if result.StatusCode != 99 || result.StatusMessage != "other" {
		t.Errorf("StatusCode/StatusMessage = %d/%q, want 99/other", result.StatusCode, result.StatusMessage)
	}
	if !strings.Contains(result.RepoStatusMessage, "Permission denied") {
		t.Errorf("RepoStatusMessage = %q, want it to contain the real PathOpenError detail", result.RepoStatusMessage)
	}
	if len(result.Backups) != 0 || result.LastBackupTime != "" {
		t.Errorf("got Backups=%v LastBackupTime=%q, want both empty — a broken repo can't report any backups", result.Backups, result.LastBackupTime)
	}
}

func TestGetPgBackRestStatusTool_BrokenRepo_RecordsObjectiveEvidence(t *testing.T) {
	withPgBackRestEvidenceRules(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	defer withMockRunner(realPgBackRestBrokenRepoJSON, nil)()

	ctx := mockToolContext{context.Background()}
	if _, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{Stanza: "main"}); err != nil {
		t.Fatalf("getPgBackRestStatusTool() error = %v", err)
	}

	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeObjectiveEvidence})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 objective_evidence event for a broken repo, got %d", len(events))
	}
	if events[0].ObjectiveEvidence == nil || events[0].ObjectiveEvidence.Signal != "pgbackrest_backup_unhealthy" {
		t.Errorf("ObjectiveEvidence = %+v, want signal=pgbackrest_backup_unhealthy", events[0].ObjectiveEvidence)
	}
}

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

// TestParsePgBackRestInfo_SingleStanzaFallback_NameMismatch verifies the
// live-discovered fallback: a single present stanza is used even when its
// name doesn't match wantStanza. Verified live (2026-09-26): a critically
// broken stanza can report Name="[invalid]" rather than its configured
// name, and that's exactly the case a caller most needs the real status
// detail from, not a generic "not found" that discards it.
func TestParsePgBackRestInfo_SingleStanzaFallback_NameMismatch(t *testing.T) {
	doc := pgBackRestJSONWithBackups("[invalid]", 99, "other", nil)
	result, err := parsePgBackRestInfo(doc, "main", time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v, want the single stanza to be used despite the name mismatch", err)
	}
	if result.StatusCode != 99 || result.StatusMessage != "other" {
		t.Errorf("got StatusCode=%d StatusMessage=%q, want 99/other", result.StatusCode, result.StatusMessage)
	}
}

// TestParsePgBackRestInfo_EmptyArray_Errors verifies the genuinely-empty
// case (repo1-path itself doesn't exist or isn't reachable at all) is a
// distinct, clearly-worded error, not silently treated the same as an
// ordinary "stanza not found."
func TestParsePgBackRestInfo_EmptyArray_Errors(t *testing.T) {
	if _, err := parsePgBackRestInfo("[]", "main", time.Hour); err == nil {
		t.Fatal("expected an error when pgbackrest info returns no stanzas at all")
	}
}

// TestParsePgBackRestInfo_RepoStatusMessage_PreferredOverGeneric verifies
// RepoStatusMessage is populated and holds the more specific detail — the
// exact shape verified live: a broken repo path produced a generic
// top-level "other" message but a detailed PathOpenError in repo[0].status.
func TestParsePgBackRestInfo_RepoStatusMessage_PreferredOverGeneric(t *testing.T) {
	doc := `[{"name":"[invalid]","status":{"code":99,"message":"other"},"repo":[{"status":{"code":99,"message":"[PathOpenError] unable to list file info for path '/var/lib/pgbackrest/backup': [13] Permission denied"}}],"backup":[]}]`
	result, err := parsePgBackRestInfo(doc, "main", time.Hour)
	if err != nil {
		t.Fatalf("parsePgBackRestInfo() error = %v", err)
	}
	if !strings.Contains(result.RepoStatusMessage, "Permission denied") {
		t.Errorf("RepoStatusMessage = %q, want it to contain the real PathOpenError detail", result.RepoStatusMessage)
	}
	if result.RepoStatusCode != 99 {
		t.Errorf("RepoStatusCode = %d, want 99", result.RepoStatusCode)
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

// sequencedRunner returns one canned output per successive Run() call
// (the last output repeats if more calls happen than outputs given),
// capturing each call's args — needed because run_pgbackrest_backup with
// no explicit stanza makes two real pgbackrest invocations (resolve the
// stanza via `info`, then `backup`), unlike every other tool in this file
// that only ever calls out once.
type sequencedRunner struct {
	outputs []string
	calls   [][]string
}

func (r *sequencedRunner) Run(_ context.Context, _ string, args []string, _ []string) (string, error) {
	r.calls = append(r.calls, args)
	idx := len(r.calls) - 1
	if idx >= len(r.outputs) {
		idx = len(r.outputs) - 1
	}
	return r.outputs[idx], nil
}

func TestRunPgBackRestBackupTool_Success(t *testing.T) {
	// Explicit Stanza skips the resolve-via-info step, isolating this test
	// to the backup-output-parsing path.
	defer withMockRunner("new backup label = 20260927-000000F\nbackup command end: completed successfully (4200ms)\n", nil)()

	ctx := mockToolContext{context.Background()}
	result, err := runPgBackRestBackupTool(ctx, RunPgBackRestBackupArgs{Stanza: "main"})
	if err != nil {
		t.Fatalf("runPgBackRestBackupTool() error = %v", err)
	}
	if !strings.Contains(result.Output, "completed successfully") {
		t.Errorf("runPgBackRestBackupTool() output = %q, want it to contain completed successfully", result.Output)
	}
}

// TestRunPgBackRestBackupTool_DefaultsToFullType verifies that with no
// stanza given, the tool first resolves the single present stanza via
// get_pgbackrest_status's own `info` call (pgbackrest's `backup` subcommand
// has no auto-detect-the-only-stanza behavior, unlike `info` — confirmed
// live against a real install, see runPgBackRestBackupImpl's comment), then
// issues the backup with --type=full by default.
func TestRunPgBackRestBackupTool_DefaultsToFullType(t *testing.T) {
	capture := &sequencedRunner{outputs: []string{
		realPgBackRestInfoJSON,
		"new backup label = 20260927-000000F\nbackup command end: completed successfully (4200ms)\n",
	}}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	if _, err := runPgBackRestBackupTool(ctx, RunPgBackRestBackupArgs{}); err != nil {
		t.Fatalf("runPgBackRestBackupTool() error = %v", err)
	}
	if len(capture.calls) != 2 {
		t.Fatalf("expected 2 pgbackrest invocations (resolve stanza, then backup), got %d: %v", len(capture.calls), capture.calls)
	}
	joined := strings.Join(capture.calls[1], " ")
	if !strings.Contains(joined, "--type=full") {
		t.Errorf("backup call args = %v, want --type=full by default", capture.calls[1])
	}
	if !strings.Contains(joined, "--stanza=main") {
		t.Errorf("backup call args = %v, want the resolved stanza name from the info call", capture.calls[1])
	}
}

func TestRunPgBackRestBackupTool_PropagatesRunnerError(t *testing.T) {
	defer withMockRunner("ERROR: [049]: unable to acquire lock", fmt.Errorf("exit status 1"))()

	ctx := mockToolContext{context.Background()}
	_, err := runPgBackRestBackupTool(ctx, RunPgBackRestBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("runPgBackRestBackupTool() error = nil, want an error when pgbackrest exits non-zero")
	}
	if !strings.Contains(err.Error(), "unable to acquire lock") {
		t.Errorf("error = %v, want it to include the pgbackrest output for diagnosis", err)
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

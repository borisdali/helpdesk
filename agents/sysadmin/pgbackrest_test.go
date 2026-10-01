package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/evidence"
	"helpdesk/internal/infra"
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
	// Regression test for a real gap found live 2026-09-30: the resource
	// extractor used to return LastBackupLabel, which is empty for a broken
	// repo (no backup was ever recorded) — since resource_named_in_quote's
	// confirmation probe treats an empty Resource as automatically
	// unconfirmed, this made confirmation structurally impossible for this
	// exact ending, regardless of how well the model's response engaged
	// with the real status_code/repo_status_message. realPgBackRestBrokenRepoJSON
	// reports its stanza as literally "[invalid]" — confirm that's what
	// gets recorded as Resource now, not an empty string.
	if events[0].ObjectiveEvidence.Resource != "[invalid]" {
		t.Errorf("ObjectiveEvidence.Resource = %q, want %q (the broken repo's own reported stanza name, not an empty LastBackupLabel)",
			events[0].ObjectiveEvidence.Resource, "[invalid]")
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

// withPgBackRestDockerInfra sets up a fake infraConfig with a Docker-hosted
// pgBackRest target, mirroring tools_test.go's own withDockerInfra (kept
// separate rather than reusing it directly since the target/container names
// here are pgBackRest-specific, not the generic "prod_db"/"alloydb-omni"
// pair the shared helper uses elsewhere in this package).
func withPgBackRestDockerInfra(t *testing.T) {
	t.Helper()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"pgbackrest_db": {
				Name:             "pgbackrest_db",
				ConnectionString: "host=localhost",
				VMName:           "pgbackrest-vm",
				ContainerName:    "helpdesk-test-pg-pgbackrest",
			},
		},
		VMs: map[string]infra.VM{
			"pgbackrest-vm": {
				Name:    "pgbackrest-vm",
				Address: "localhost",
				Runtime: "docker",
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })
}

// TestGetPgBackRestStatusTool_DockerDispatch verifies the docker/podman
// dispatch path builds the exact command form pgBackRest requires: run as
// the postgres OS user (pgBackRest checks PGDATA ownership, so it can't run
// as docker exec's default root) via a single safely-quoted shell command
// string, not raw argv — this is the same "su postgres -c '...'" shape
// manually verified live against the real container during development.
func TestGetPgBackRestStatusTool_DockerDispatch(t *testing.T) {
	withPgBackRestDockerInfra(t)
	capture := &sequencedRunner{outputs: []string{realPgBackRestInfoJSON}}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	result, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest_db"})
	if err != nil {
		t.Fatalf("getPgBackRestStatusTool() error = %v", err)
	}
	if result.Stanza != "main" {
		t.Errorf("Stanza = %q, want main", result.Stanza)
	}
	if len(capture.calls) != 1 {
		t.Fatalf("expected 1 docker exec call, got %d: %v", len(capture.calls), capture.calls)
	}
	args := capture.calls[0]
	if len(args) < 2 || args[0] != "exec" || args[1] != "helpdesk-test-pg-pgbackrest" {
		t.Fatalf("docker args = %v, want [exec helpdesk-test-pg-pgbackrest ...]", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "su") || !strings.Contains(joined, "postgres") {
		t.Errorf("docker args = %v, want it to run as the postgres OS user via su", args)
	}
	if !strings.Contains(joined, "pgbackrest") || !strings.Contains(joined, "info") || !strings.Contains(joined, "--output=json") {
		t.Errorf("docker args = %v, want a quoted pgbackrest info --output=json invocation", args)
	}
}

// TestGetPgBackRestStatusTool_UnknownTarget_ReturnsResolveHostError verifies
// resolveHost's own "server not found" error propagates rather than being
// swallowed or misreported as a parse failure.
func TestGetPgBackRestStatusTool_UnknownTarget_ReturnsResolveHostError(t *testing.T) {
	withPgBackRestDockerInfra(t)
	ctx := mockToolContext{context.Background()}
	_, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{Target: "nonexistent"})
	if err == nil {
		t.Fatal("getPgBackRestStatusTool() error = nil, want an error for an unknown target")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want resolveHost's own \"not found\" message", err)
	}
}

// TestGetPgBackRestStatusTool_EmptyTargetWithInfraConfig_Errors is a
// regression test for a real gap found live 2026-09-30: a model given a
// connection_string that didn't match any known infrastructure entry
// reasoned its way into omitting target rather than escalating, which
// silently ran pgbackrest against the agent's own host (no Docker/SSH
// involved at all) instead of erroring. Once infrastructure config is
// loaded, an empty target must be treated as a resolution failure, not a
// valid "run locally" request — that request is still supported when no
// infrastructure config exists at all (see
// TestGetPgBackRestStatusTool_Success, which deliberately leaves
// infraConfig nil).
func TestGetPgBackRestStatusTool_EmptyTargetWithInfraConfig_Errors(t *testing.T) {
	withPgBackRestDockerInfra(t)
	ctx := mockToolContext{context.Background()}
	_, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{})
	if err == nil {
		t.Fatal("getPgBackRestStatusTool() error = nil, want an error when infraConfig is loaded but target is empty")
	}
	if !strings.Contains(err.Error(), "target is required") {
		t.Errorf("error = %v, want it to explain target is required when infra config is loaded", err)
	}
}

// TestRunPgBackRestBackupTool_DockerDispatch mirrors
// TestGetPgBackRestStatusTool_DockerDispatch for the write-side tool: with an
// explicit stanza (isolating this test to the dispatch shape, not stanza
// resolution — that's covered separately by
// TestRunPgBackRestBackupTool_DefaultsToFullType), confirms the docker
// exec + su postgres -c invocation carries the right pgbackrest backup args.
func TestRunPgBackRestBackupTool_DockerDispatch(t *testing.T) {
	withPgBackRestDockerInfra(t)
	capture := &sequencedRunner{outputs: []string{"backup command end: completed successfully (100ms)\n"}}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	if _, err := runPgBackRestBackupTool(ctx, RunPgBackRestBackupArgs{Target: "pgbackrest_db", Stanza: "main"}); err != nil {
		t.Fatalf("runPgBackRestBackupTool() error = %v", err)
	}
	if len(capture.calls) != 1 {
		t.Fatalf("expected 1 docker exec call, got %d: %v", len(capture.calls), capture.calls)
	}
	args := capture.calls[0]
	if len(args) < 2 || args[0] != "exec" || args[1] != "helpdesk-test-pg-pgbackrest" {
		t.Fatalf("docker args = %v, want [exec helpdesk-test-pg-pgbackrest ...]", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "su") || !strings.Contains(joined, "postgres") {
		t.Errorf("docker args = %v, want it to run as the postgres OS user via su", args)
	}
	if !strings.Contains(joined, "--stanza=main") || !strings.Contains(joined, "--type=full") || !strings.Contains(joined, "backup") {
		t.Errorf("docker args = %v, want a quoted pgbackrest --stanza=main --type=full backup invocation", args)
	}
}

// withPgBackRestSSHInfra sets up a fake infraConfig with an SSH-reachable
// pgBackRest target pointed at a real in-process SSH server (srv/keyPath),
// proving get_pgbackrest_status/run_pgbackrest_backup correctly reach C1's
// SSH capability end to end — the first real consumer of runOnHost, not
// just sshexec_test.go's own generic exercise of the SSH mechanism itself.
// A non-empty SystemdUnit is required only because resolveHost's own
// validation demands one for any non-docker/podman VM runtime (systemd is
// the assumed use case there); it plays no role in the SSH dispatch itself.
func withPgBackRestSSHInfra(t *testing.T, addr string, port int, keyPath string) {
	t.Helper()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"pgbackrest_db": {
				Name:             "pgbackrest_db",
				ConnectionString: "host=localhost",
				VMName:           "pgbackrest-vm",
				SystemdUnit:      "postgresql-16",
			},
		},
		VMs: map[string]infra.VM{
			"pgbackrest-vm": {
				Name:       "pgbackrest-vm",
				Address:    addr,
				SSHUser:    "testuser",
				SSHPort:    port,
				SSHKeyPath: keyPath,
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })
}

// TestGetPgBackRestStatusTool_SSHDispatch proves get_pgbackrest_status
// reaches a real remote host over a real SSH round trip (real TCP, real
// handshake, real public-key auth, real exec channel) when the resolved
// host has no docker/podman runtime but does carry SSH connection details —
// mirroring sshexec_test.go's own real-server pattern rather than mocking
// the SSH layer away.
func TestGetPgBackRestStatusTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "pgbackrest") || !strings.Contains(cmd, "info") || !strings.Contains(cmd, "--output=json") {
			return "unexpected command: " + cmd, 1
		}
		return realPgBackRestInfoJSON, 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := getPgBackRestStatusTool(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest_db"})
	if err != nil {
		t.Fatalf("getPgBackRestStatusTool() error = %v", err)
	}
	if result.Stanza != "main" {
		t.Errorf("Stanza = %q, want main", result.Stanza)
	}
}

// TestRunPgBackRestBackupTool_SSHDispatch mirrors
// TestGetPgBackRestStatusTool_SSHDispatch for the write-side tool.
func TestRunPgBackRestBackupTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "pgbackrest") || !strings.Contains(cmd, "--stanza=main") || !strings.Contains(cmd, "backup") {
			return "unexpected command: " + cmd, 1
		}
		return "backup command end: completed successfully (100ms)\n", 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := runPgBackRestBackupTool(ctx, RunPgBackRestBackupArgs{Target: "pgbackrest_db", Stanza: "main"})
	if err != nil {
		t.Fatalf("runPgBackRestBackupTool() error = %v", err)
	}
	if !strings.Contains(result.Output, "completed successfully") {
		t.Errorf("Output = %q, want it to contain completed successfully", result.Output)
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

// newDenyHostWriteEnforcer mirrors tools_test.go's own
// newDenyHostDestructiveEnforcer, for the write action class specifically —
// run_pgbackrest_backup is ActionWrite, not ActionDestructive (see
// docs/SYSADMIN_AGENT.md §4.2), so a destructive-only deny rule would not
// exercise its policy check at all.
func newDenyHostWriteEnforcer(t *testing.T) *agentutil.PolicyEnforcer {
	t.Helper()
	const yaml = `
version: "1"
policies:
  - name: deny-host-write
    resources:
      - type: host
    rules:
      - action: write
        effect: deny
        message: "host write operations are not permitted in this test"
`
	path := writeTempSysadminPolicyFile(t, yaml)
	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    path,
		DefaultPolicy: "allow",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

// TestRunPgBackRestBackupTool_PolicyDenied is a regression test for a real
// gap found documenting this tool: unlike restart_container/restart_service,
// runPgBackRestBackupImpl called no policyEnforcer.CheckTool at all when
// first written — a real write action bypassing operating-mode and
// tag-based policy rules entirely, with only the remediation playbook's own
// approval_mode=manual standing between a proposal and execution. Fixed by
// adding the same CheckTool call restart_container/restart_service already
// make, with ActionWrite in place of ActionDestructive.
func TestRunPgBackRestBackupTool_PolicyDenied(t *testing.T) {
	withPgBackRestDockerInfra(t)
	defer withMockRunner("", nil)()
	defer withSysadminPolicyEnforcer(newDenyHostWriteEnforcer(t))()

	_, err := runPgBackRestBackupImpl(context.Background(), RunPgBackRestBackupArgs{
		Target: "pgbackrest_db",
		Stanza: "main",
	})
	if err == nil {
		t.Fatal("expected policy denial, got nil error")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("error %q should mention 'not permitted'", err.Error())
	}
}

// TestRunPgBackRestBackupTool_EmptyTargetWithInfraConfig_Errors mirrors
// TestGetPgBackRestStatusTool_EmptyTargetWithInfraConfig_Errors for the
// write-side tool. Explicit Stanza isolates this to the backup-dispatch
// switch itself, not the stanza-resolution call (which reuses
// getPgBackRestStatusImpl and would otherwise hit the same error one layer
// up, via a different call path, before this function's own switch is ever
// reached).
func TestRunPgBackRestBackupTool_EmptyTargetWithInfraConfig_Errors(t *testing.T) {
	withPgBackRestDockerInfra(t)
	_, err := runPgBackRestBackupImpl(context.Background(), RunPgBackRestBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("runPgBackRestBackupImpl() error = nil, want an error when infraConfig is loaded but target is empty")
	}
	if !strings.Contains(err.Error(), "target is required") {
		t.Errorf("error = %v, want it to explain target is required when infra config is loaded", err)
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

// TestGetPgBackRestStatusResult_BackupStaleFalse_SerializesExplicitly is a
// regression test for a real governance gap found live 2026-09-30: with
// `omitempty` on BackupStale's JSON tag, a healthy/fresh backup's
// backup_stale=false was silently dropped from the tool's JSON output. A
// model reading that result couldn't tell "confirmed not stale" apart from
// "field not computed," and reasoned its way into calling
// run_pgbackrest_backup on an already-current backup instead of following
// its own playbook's Ending A ("already healthy, nothing to do"). A write
// action should never hinge on the model correctly reconstructing a boolean
// from its absence — assert the field is always present in the raw JSON,
// not just correct in the Go struct.
func TestGetPgBackRestStatusResult_BackupStaleFalse_SerializesExplicitly(t *testing.T) {
	result := GetPgBackRestStatusResult{Stanza: "main", StatusCode: 0, BackupStale: false, LastBackupError: false}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"backup_stale":false`) {
		t.Errorf("marshaled JSON = %s, want it to explicitly contain \"backup_stale\":false, not omit it", raw)
	}
	if !strings.Contains(string(raw), `"last_backup_error":false`) {
		t.Errorf("marshaled JSON = %s, want it to explicitly contain \"last_backup_error\":false, not omit it", raw)
	}
}

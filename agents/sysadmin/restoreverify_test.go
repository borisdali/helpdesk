package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

// vmRecoveryLog is the real Ubuntu VM's recovery after restore_from_backup
// (2026-10-18, plr_4d5bdb81), from /var/log/postgresql/postgresql-16-main.log.
// Note the last "restored log file" line overall is a .history file AFTER the
// timeline switch, and 0000001B000000000000000C is restored twice.
const vmRecoveryLog = `2026-10-18 16:23:39.607 EDT [2562968] LOG:  restored log file "0000001D.history" from archive
2026-10-18 16:23:39.620 EDT [2562968] LOG:  restored log file "0000001D.history" from archive
2026-10-18 16:23:39.658 EDT [2562968] LOG:  restored log file "0000001B000000000000000C" from archive
2026-10-18 16:23:39.677 EDT [2562968] LOG:  restored log file "0000001B.history" from archive
2026-10-18 16:23:39.688 EDT [2562968] LOG:  restored log file "0000001C.history" from archive
2026-10-18 16:23:39.718 EDT [2562968] LOG:  consistent recovery state reached at 0/C000138
2026-10-18 16:23:39.737 EDT [2562968] LOG:  redo done at 0/C000138 system usage: CPU: user: 0.00 s, system: 0.00 s, elapsed: 0.04 s
2026-10-18 16:23:39.772 EDT [2562968] LOG:  restored log file "0000001B000000000000000C" from archive
2026-10-18 16:23:39.788 EDT [2562968] LOG:  selected new timeline ID: 30
2026-10-18 16:23:39.807 EDT [2562968] LOG:  restored log file "0000001D.history" from archive
2026-10-18 16:23:39.900 EDT [2562965] LOG:  database system is ready to accept connections`

func TestParseRecoveryLog_RealVM(t *testing.T) {
	rec := parseRecoveryLog(vmRecoveryLog)
	want := recoveryLog{Found: true, LastRestoredWAL: "0000001B000000000000000C", NewTimeline: 30,
		RedoDoneLSN: "0/C000138", ConsistentLSN: "0/C000138"}
	if rec != want {
		t.Errorf("parseRecoveryLog = %+v, want %+v", rec, want)
	}
}

// Two recoveries in one log (an earlier restore, then this one): only the
// newest counts, and the older one's segments must not leak into it.
func TestParseRecoveryLog_NewestRecoveryOnly(t *testing.T) {
	log := `LOG:  restored log file "000000010000000000000009" from archive
LOG:  redo done at 0/9000100
LOG:  selected new timeline ID: 2
LOG:  database system is ready to accept connections
LOG:  restored log file "000000020000000000000004" from archive
LOG:  redo done at 0/4000060
LOG:  selected new timeline ID: 3`
	rec := parseRecoveryLog(log)
	if rec.LastRestoredWAL != "000000020000000000000004" || rec.NewTimeline != 3 || rec.RedoDoneLSN != "0/4000060" {
		t.Errorf("parseRecoveryLog = %+v, want the timeline-3 recovery only", rec)
	}

	// The newest recovery restored nothing from the archive: the older
	// recovery's segment must not be reported as this one's.
	rec = parseRecoveryLog(`LOG:  restored log file "000000010000000000000009" from archive
LOG:  selected new timeline ID: 2
LOG:  redo done at 0/4000060
LOG:  selected new timeline ID: 3`)
	if rec.LastRestoredWAL != "" || rec.NewTimeline != 3 {
		t.Errorf("parseRecoveryLog = %+v, want timeline 3 with no restored segment", rec)
	}
}

func TestParseRecoveryLog_NoPromotion(t *testing.T) {
	for name, log := range map[string]string{
		"empty":              "",
		"still recovering":   `LOG:  restored log file "000000010000000000000009" from archive`,
		"history files only": `LOG:  restored log file "00000002.history" from archive`,
		"normal startup":     "LOG:  database system is ready to accept connections",
	} {
		if rec := parseRecoveryLog(log); rec.Found {
			t.Errorf("%s: parseRecoveryLog = %+v, want not found", name, rec)
		}
	}
}

func TestCompareRecovery(t *testing.T) {
	pred := &RestorePrediction{RecoverToWAL: "0000001B000000000000000C", ExpectedTimeline: 30}
	real := parseRecoveryLog(vmRecoveryLog)
	cases := []struct {
		name       string
		pred       *RestorePrediction
		rec        recoveryLog
		wantStatus string
		wantDetail string
	}{
		{"real VM restore, as predicted", pred, real, RestoreVerified, "redo done at 0/C000138"},
		{"stopped early", pred, recoveryLog{Found: true, LastRestoredWAL: "0000001B000000000000000A", NewTimeline: 30}, RestoreMismatch, "stopped early"},
		{"went past", pred, recoveryLog{Found: true, LastRestoredWAL: "0000001B000000000000000D", NewTimeline: 30}, RestoreMismatch, "went past the predicted end"},
		{"unexpected newer timeline", pred, recoveryLog{Found: true, LastRestoredWAL: "0000001B000000000000000C", NewTimeline: 31}, RestoreMismatch, "expected 30"},
		{"log predates the restart", pred, recoveryLog{Found: true, LastRestoredWAL: "0000001B000000000000000C", NewTimeline: 29}, RestoreNotFound, "predates the restart"},
		{"no recovery in the log", pred, recoveryLog{}, RestoreNotFound, "no completed recovery"},
		{"no prediction recorded", nil, real, RestoreUnverified, "no restore_from_backup prediction"},
		{"nothing restored from the archive", pred, recoveryLog{Found: true, NewTimeline: 30}, RestoreUnverified, "no WAL segment restored"},
	}
	for _, c := range cases {
		status, detail := compareRecovery(c.pred, c.rec)
		if status != c.wantStatus || !strings.Contains(detail, c.wantDetail) {
			t.Errorf("%s: compareRecovery = (%s, %q), want %s containing %q", c.name, status, detail, c.wantStatus, c.wantDetail)
		}
	}
}

func TestRestorePredictionFrom(t *testing.T) {
	now := time.Date(2026, 10, 18, 20, 23, 35, 0, time.UTC)
	wal := &WALContinuity{Status: WALContinuous, BackupLabel: "20261017-152429F",
		ArchiveEndWAL: "0000001B000000000000000C", ArchiveEndTime: "2026-10-17T19:24:32Z", LatestTimeline: 29}
	p := restorePredictionFrom(wal, now)
	if p == nil || p.RecoverToWAL != "0000001B000000000000000C" || p.ExpectedTimeline != 30 ||
		p.BackupLabel != "20261017-152429F" || p.PredictedAt != "2026-10-18T20:23:35Z" {
		t.Errorf("restorePredictionFrom = %+v", p)
	}
	for name, w := range map[string]*WALContinuity{
		"nil":          nil,
		"broken":       {Status: WALBroken, ArchiveEndWAL: "x", LatestTimeline: 1},
		"no end point": {Status: WALContinuous, LatestTimeline: 1},
	} {
		if p := restorePredictionFrom(w, now); p != nil {
			t.Errorf("%s: restorePredictionFrom = %+v, want nil", name, p)
		}
	}
}

// verifyRestoreRecoveryImpl end to end against fake host commands, for a
// systemd target: the journal has only systemd's own lines, so the recovery
// must be read from the PostgreSQL log file — the VM's real layout.
func TestVerifyRestoreRecovery_SystemdReadsLogFile(t *testing.T) {
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"vm": {Name: "vm", ConnectionString: "host=localhost", VMName: "vm-host",
				SystemdUnit: "postgresql@16-main", PgLogDir: "/var/log/postgresql"},
		},
		VMs: map[string]infra.VM{"vm-host": {Name: "vm-host", Address: "localhost"}},
	}
	t.Cleanup(func() { infraConfig = nil })
	restorePredictions.Lock()
	delete(restorePredictions.byTarget, "vm")
	restorePredictions.Unlock()
	t.Cleanup(func() {
		restorePredictions.Lock()
		delete(restorePredictions.byTarget, "vm")
		restorePredictions.Unlock()
	})

	old := cmdRunner
	cmdRunner = runnerFunc(func(_ context.Context, name string, args []string, _ []string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "journalctl"):
			return "Oct 18 16:23:41 libra systemd[1]: Started postgresql@16-main.service - PostgreSQL Cluster 16-main.", nil
		case strings.Contains(joined, "ls -t"):
			return "postgresql-16-main.log\n", nil
		case strings.Contains(joined, "tail"):
			return vmRecoveryLog, nil
		}
		return "", nil
	})
	t.Cleanup(func() { cmdRunner = old })

	// No prediction yet: reads the real recovery but can't compare it.
	res, err := verifyRestoreRecoveryImpl(context.Background(), VerifyRestoreRecoveryArgs{Target: "vm"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != RestoreUnverified || res.ReachedWAL != "0000001B000000000000000C" || res.LogSource != "PostgreSQL log file" {
		t.Errorf("without a prediction: %+v", res)
	}

	saveRestorePrediction("vm", RestorePrediction{RecoverToWAL: "0000001B000000000000000C", ExpectedTimeline: 30})
	res, err = verifyRestoreRecoveryImpl(context.Background(), VerifyRestoreRecoveryArgs{Target: "vm"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != RestoreVerified || res.NewTimeline != 30 || res.RedoDoneLSN != "0/C000138" {
		t.Errorf("with the prediction: %+v", res)
	}
}

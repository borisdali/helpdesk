package main

import (
	"strings"
	"testing"
	"time"
)

var pitrNow = time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)

func TestRecoveryTarget_Normalize(t *testing.T) {
	tests := []struct {
		name    string
		in      RecoveryTarget
		want    string
		wantErr string
	}{
		{"utc time", RecoveryTarget{TargetTime, "2026-10-27T14:05:00Z"}, "2026-10-27 14:05:00+00", ""},
		{"offset converted to utc", RecoveryTarget{TargetTime, "2026-10-27T16:05:00+02:00"}, "2026-10-27 14:05:00+00", ""},
		{"fractional seconds", RecoveryTarget{TargetTime, "2026-10-27T14:05:00.25Z"}, "2026-10-27 14:05:00.25+00", ""},
		{"no zone", RecoveryTarget{TargetTime, "2026-10-27 14:05:00"}, "", "RFC3339"},
		{"future", RecoveryTarget{TargetTime, "2026-10-27T15:00:01Z"}, "", "future"},
		{"lsn", RecoveryTarget{TargetLSN, "0/3000028"}, "0/3000028", ""},
		{"lsn lower case normalized", RecoveryTarget{TargetLSN, "a/bc"}, "A/BC", ""},
		{"lsn malformed", RecoveryTarget{TargetLSN, "3000028"}, "", "invalid LSN"},
		{"missing type", RecoveryTarget{"", "0/1"}, "", "required"},
		{"xid unsupported", RecoveryTarget{"xid", "1234"}, "", "not supported"},
		{"name unsupported", RecoveryTarget{"name", "before_drop"}, "", "not supported"},
		// Shell syntax must never get through, in either type.
		{"injection in time", RecoveryTarget{TargetTime, "2026-10-27T14:05:00Z'; touch /tmp/x; '"}, "", "RFC3339"},
		{"injection in lsn", RecoveryTarget{TargetLSN, "0/1; touch /tmp/x"}, "", "invalid LSN"},
		{"injection via lsn newline", RecoveryTarget{TargetLSN, "0/1\ntouch /tmp/x"}, "", "invalid LSN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.Normalize(pitrNow)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func pitrBackups() []PgBackRestBackup {
	at := func(h int) time.Time { return time.Date(2026, 10, 27, h, 0, 0, 0, time.UTC) }
	return []PgBackRestBackup{
		{Label: "20261027-060000F", Type: "full", StopTime: at(6), LSNStop: "0/5000000"},
		{Label: "20261027-100000F_20261027-120000I", Type: "incr", StopTime: at(12), LSNStop: "0/9000000"},
		{Label: "20261027-130000F", Type: "full", StopTime: at(13), LSNStop: "0/B000000", Error: true},
	}
}

func TestSelectPITRBackup(t *testing.T) {
	tests := []struct {
		name    string
		target  RecoveryTarget
		want    string
		wantErr string
	}{
		{"newest before target", RecoveryTarget{TargetTime, "2026-10-27T14:05:00Z"}, "20261027-100000F_20261027-120000I", ""},
		{"errored backup is skipped", RecoveryTarget{TargetTime, "2026-10-27T14:59:00Z"}, "20261027-100000F_20261027-120000I", ""},
		{"earlier target picks earlier backup", RecoveryTarget{TargetTime, "2026-10-27T09:00:00Z"}, "20261027-060000F", ""},
		{"backup stop equal to target does not qualify", RecoveryTarget{TargetTime, "2026-10-27T06:00:00Z"}, "", "no backup finished before"},
		{"before every backup", RecoveryTarget{TargetTime, "2026-10-27T01:00:00Z"}, "", "no backup finished before"},
		{"lsn", RecoveryTarget{TargetLSN, "0/A000000"}, "20261027-100000F_20261027-120000I", ""},
		{"lsn equal to stop does not qualify", RecoveryTarget{TargetLSN, "0/5000000"}, "", "no backup finished before"},
		{"future time", RecoveryTarget{TargetTime, "2026-10-28T00:00:00Z"}, "", "future"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectPITRBackup(pitrBackups(), tc.target, pitrNow)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Label != tc.want {
				t.Fatalf("got %s, want %s", got.Label, tc.want)
			}
		})
	}
	if _, err := selectPITRBackup(nil, RecoveryTarget{TargetTime, "2026-10-27T14:05:00Z"}, pitrNow); err == nil || !strings.Contains(err.Error(), "no usable backup") {
		t.Fatalf("empty repo: err = %v", err)
	}
}

func TestPITRRestoreCommand(t *testing.T) {
	tgt := RecoveryTarget{TargetTime, "2026-10-27T14:05:00Z"}
	norm, _ := tgt.Normalize(pitrNow)
	got := pitrRestoreCommand("main", "20261027-060000F", tgt, norm, 1)
	want := `pgbackrest --stanza='main' --delta --set='20261027-060000F' --type=time --target='2026-10-27 14:05:00+00' --target-timeline=1 --target-action=promote restore`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The restore-to-latest path must keep checking the newest backup.
func TestWALCheckInputFor(t *testing.T) {
	r := GetPgBackRestStatusResult{Stanza: "main", Backups: pitrBackups()}
	newest, ok := r.walCheckInput()
	if !ok || newest.BackupLabel != "20261027-130000F" {
		t.Fatalf("walCheckInput() = %+v, %v; want the newest backup", newest, ok)
	}
	sel, ok := r.walCheckInputFor("20261027-060000F")
	if !ok || sel.BackupLabel != "20261027-060000F" || sel.LSNStop != "0/5000000" {
		t.Fatalf("walCheckInputFor = %+v, %v", sel, ok)
	}
	if _, ok := r.walCheckInputFor("nope"); ok {
		t.Fatal("unknown label must not match")
	}
}

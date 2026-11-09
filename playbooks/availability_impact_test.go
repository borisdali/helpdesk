package playbooks

import (
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// systemSeries returns the series_id of every embedded system playbook.
func systemSeries(t *testing.T) []string {
	t.Helper()
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := FS.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		var y systemPlaybookYAML
		if err := yaml.Unmarshal(data, &y); err != nil || y.SeriesID == "" {
			t.Fatalf("%s: series_id %q, %v", e.Name(), y.SeriesID, err)
		}
		out = append(out, y.SeriesID)
	}
	sort.Strings(out)
	return out
}

// A playbook added without a classification would be reported as unclassified
// by the ledger, forever. Fail here instead, so the author decides.
func TestAvailabilityImpact_CoversEverySystemSeries(t *testing.T) {
	series := systemSeries(t)
	if len(series) < 40 {
		t.Fatalf("found only %d system series; the embed is broken", len(series))
	}
	for _, id := range series {
		impact, ok := AvailabilityImpact(id)
		if !ok {
			t.Errorf("%s has no availability impact: add it to availabilityImpact in availability_impact.go (outage, degraded or none)", id)
			continue
		}
		if !impact.Valid() {
			t.Errorf("%s has an invalid impact %q", id, impact)
		}
	}
}

// And the other way: a renamed or removed series must not leave a stale line.
func TestAvailabilityImpact_HasNoStaleEntries(t *testing.T) {
	known := map[string]bool{}
	for _, id := range systemSeries(t) {
		known[id] = true
	}
	for id, impact := range availabilityImpact {
		if !known[id] {
			t.Errorf("availabilityImpact has %q (%s), which is not a system playbook series", id, impact)
		}
		if !impact.Valid() {
			t.Errorf("%s: invalid impact %q", id, impact)
		}
	}
}

// The classifications the budget leans on hardest, pinned by name: a restore, a
// restart or a pod that is not running is an outage, and housekeeping is not.
func TestAvailabilityImpact_AnchorSeries(t *testing.T) {
	for id, want := range map[string]Impact{
		"pbs_pgbackrest_restore_remediate": ImpactOutage,
		"pbs_pgbackrest_pitr_remediate":    ImpactOutage,
		"pbs_db_restart_action":            ImpactOutage,
		"pbs_db_restart_triage":            ImpactOutage,
		"pbs_k8s_scale_to_zero_remediate":  ImpactOutage,
		"pbs_k8s_pod_crash_remediate":      ImpactOutage,
		"pbs_wal_disk_full":                ImpactOutage,
		"pbs_lock_chain_remediate":         ImpactDegraded,
		"pbs_replica_restart_action":       ImpactDegraded,
		"pbs_vacuum_remediate":             ImpactNone,
		"pbs_pgbackrest_backup_remediate":  ImpactNone,
		// A triage that only reads is not charged for the restore it may lead to.
		"pbs_pgbackrest_pitr_triage": ImpactNone,
	} {
		if got, ok := AvailabilityImpact(id); !ok || got != want {
			t.Errorf("%s = %q (declared %v), want %q", id, got, ok, want)
		}
	}
}

func TestAvailabilityImpact_UndeclaredSeriesIsNotGuessed(t *testing.T) {
	for _, id := range []string{"", "pbs_made_up", "pbs_deadbeef", "PBS_VACUUM_REMEDIATE"} {
		if got, ok := AvailabilityImpact(id); ok || got != "" {
			t.Errorf("AvailabilityImpact(%q) = %q, %v; want nothing", id, got, ok)
		}
	}
}

func TestImpact_Valid(t *testing.T) {
	for _, i := range []Impact{ImpactOutage, ImpactDegraded, ImpactNone} {
		if !i.Valid() {
			t.Errorf("%q should be valid", i)
		}
	}
	for _, i := range []Impact{"", "Outage", "down", "partial", " outage"} {
		if i.Valid() {
			t.Errorf("%q should not be valid", i)
		}
	}
}

func TestWorstImpact(t *testing.T) {
	tests := []struct {
		name string
		in   []Impact
		want Impact
		ok   bool
	}{
		{"nothing", nil, "", false},
		{"one hop", []Impact{ImpactDegraded}, ImpactDegraded, true},
		{"the restore after a triage that only read", []Impact{ImpactNone, ImpactOutage}, ImpactOutage, true},
		{"the order of the hops does not matter", []Impact{ImpactOutage, ImpactNone}, ImpactOutage, true},
		{"degraded over none", []Impact{ImpactNone, ImpactDegraded, ImpactNone}, ImpactDegraded, true},
		{"outage over everything", []Impact{ImpactDegraded, ImpactOutage, ImpactNone, ImpactDegraded}, ImpactOutage, true},
		{"an unclassified hop is ignored, not charged", []Impact{"", ImpactNone}, ImpactNone, true},
		{"a bad value is ignored", []Impact{"bogus", ImpactDegraded}, ImpactDegraded, true},
		{"only unclassified hops: no verdict", []Impact{"", "bogus"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WorstImpact(tc.in...)
			if got != tc.want || ok != tc.ok {
				t.Errorf("WorstImpact(%v) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The PITR chain end to end: the triage reads, the remediation restores, and the
// incident is an outage because of the hop that actually stopped the database.
func TestAvailabilityImpact_PITRChain(t *testing.T) {
	triage, _ := AvailabilityImpact("pbs_pgbackrest_pitr_triage")
	remediate, _ := AvailabilityImpact("pbs_pgbackrest_pitr_remediate")
	if got, _ := WorstImpact(triage); got != ImpactNone {
		t.Errorf("a PITR triage that never reached the restore is %q, want none", got)
	}
	if got, _ := WorstImpact(triage, remediate); got != ImpactOutage {
		t.Errorf("a PITR triage and restore is %q, want outage", got)
	}
}

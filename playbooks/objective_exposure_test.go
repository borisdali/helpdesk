package playbooks_test

import (
	"strings"
	"testing"
)

// The objective-exposure check (v0.35) is read-only and meant to run on a
// schedule. These tests pin the parts of its prose the gateway relies on: it
// can never hand on to a restore, and only the two endings that need nothing
// from a person carry the marker that records the run as resolved. Without it
// every scheduled run would sit in the incidents list as needs_human.

const exposureFile = "pgbackrest-objective-exposure-triage.yaml"

func TestObjectiveExposure_IsReadOnlyAndTerminal(t *testing.T) {
	pb := loadPITR(t, exposureFile)
	if pb.SeriesID != "pbs_pgbackrest_objective_exposure_triage" {
		t.Fatalf("series_id = %q", pb.SeriesID)
	}
	if len(pb.TransitionsTo) != 0 {
		t.Fatalf("transitions_to = %v; a check that only reads must not be able to hand on to anything", pb.TransitionsTo)
	}
	if strings.Contains(pb.Guidance, "TRANSITION_TO") {
		t.Fatal("guidance tells the model to transition")
	}
	for _, tool := range []string{"restore_from_backup", "restore_to_point_in_time", "run_pgbackrest_backup"} {
		if strings.Contains(pb.Guidance, tool) {
			t.Fatalf("guidance names %s; the check must not point the model at a mutating tool", tool)
		}
	}
}

func TestObjectiveExposure_OnlyQuietEndingsAreRecordedResolved(t *testing.T) {
	pb := loadPITR(t, exposureFile)
	endings := map[string]string{}
	var cur string
	for _, line := range strings.Split(pb.Guidance, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Ending ") {
			cur = trim[:len("Ending X")]
			continue
		}
		if cur != "" && strings.HasPrefix(trim, "FINDINGS:") {
			endings[cur] = trim
		}
	}
	if len(endings) != 4 {
		t.Fatalf("found FINDINGS for %d endings, want 4: %v", len(endings), endings)
	}
	for ending, wantMarker := range map[string]bool{
		"Ending A": true,  // within
		"Ending B": true,  // not assessed
		"Ending C": false, // the history breaks the rto
		"Ending D": false, // the archive is older than the rpo
	} {
		got := strings.Contains(endings[ending], "recommended=no_changes_needed")
		if got != wantMarker {
			t.Errorf("%s: recommended=no_changes_needed present = %v, want %v (%s)", ending, got, wantMarker, endings[ending])
		}
	}
}

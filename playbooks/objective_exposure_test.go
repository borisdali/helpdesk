package playbooks_test

import (
	"strings"
	"testing"

	"helpdesk/playbooks"
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
	if !strings.Contains(pb.Guidance, "whether the server is idle or archiving has stopped is not known") {
		t.Error("Ending D no longer tells the model not to claim idle or stopped: the first live run wrote 'the database has not generated or archived WAL' as its primary hypothesis")
	}
	if !strings.Contains(strings.Join(strings.Fields(pb.Guidance), " "), "ONE sentence copied word for word from a detail field") {
		t.Error("guidance no longer asks for a verbatim detail sentence as evidence: the first live run joined field names with semicolons and was flagged as unverified evidence")
	}
	if !strings.Contains(strings.Join(strings.Fields(pb.Guidance), " "), "Never add a HYPOTHESIS_2 only to reject it") {
		t.Error("guidance no longer limits a run to one hypothesis: the third live run added the rto as a second hypothesis and rejected it as 'not an alternative'")
	}
	if strings.Contains(pb.Guidance, "HYPOTHESIS_2:") {
		t.Error("an ending template contains a HYPOTHESIS_2 line, contradicting the one-hypothesis rule")
	}
	if !strings.Contains(pb.Guidance, "first and only tool call") {
		t.Error("guidance no longer says get_pgbackrest_status is the only tool call; a first run called check_host first")
	}
	for _, tool := range []string{"restore_from_backup", "restore_to_point_in_time", "run_pgbackrest_backup"} {
		if strings.Contains(pb.Guidance, tool) {
			t.Fatalf("guidance names %s; the check must not point the model at a mutating tool", tool)
		}
	}
}

// The attribution classifier files a run under one of the root-cause classes by
// their descriptions. A live run with the rpo within and the rto not comparable
// was filed under objectives-not-assessed, because that description named "not
// enough restore history" as a reason. Pin the wording that keeps the two apart.
func TestObjectiveExposure_WithinAndNotAssessedAreNotConfusable(t *testing.T) {
	raw, err := playbooks.FS.ReadFile(exposureFile)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"The other check may be not comparable",
		"Neither check could be made",
		"If either check was made and is within its objective, the class is objectives-within, not this one",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("class descriptions no longer say %q: a run with only the rpo checked was filed as not assessed", want)
		}
	}
	if strings.Contains(text, "or there is not enough restore history to compare with the rto. This is a statement") {
		t.Error("objectives-not-assessed again lists missing restore history as a reason on its own")
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
	if len(endings) != 5 {
		t.Fatalf("found FINDINGS for %d endings, want 5: %v", len(endings), endings)
	}
	for ending, wantMarker := range map[string]bool{
		"Ending A": true,  // within
		"Ending B": true,  // not assessed
		"Ending C": false, // the history breaks the rto
		"Ending D": false, // the archive is older than the rpo
		"Ending E": false, // the status could not be read: the check did not run
	} {
		got := strings.Contains(endings[ending], "recommended=no_changes_needed")
		if got != wantMarker {
			t.Errorf("%s: recommended=no_changes_needed present = %v, want %v (%s)", ending, got, wantMarker, endings[ending])
		}
	}
}

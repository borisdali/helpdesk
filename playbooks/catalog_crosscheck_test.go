package playbooks_test

import (
	"context"
	"os"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/playbooks"
	"helpdesk/testing/faultlib"
)

// findCatalogFromPlaybooksDir locates testing/catalog/failures.yaml relative
// to this package's own directory, mirroring testing/faultlib/faultlib_test.go's
// own findCatalog helper (that one is unexported and lives in a different
// package, so it can't be reused directly).
func findCatalogFromPlaybooksDir() string {
	paths := []string{
		"../testing/catalog/failures.yaml",
		"testing/catalog/failures.yaml",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestCatalogPlaybookReferencesExist cross-validates every playbook's own
// transitions_to/escalates_to targets, and every fault's
// diagnosis_playbook_series_id/remediation.playbook_id, against the real
// seeded playbook set — catching a stale or typo'd series_id reference at
// `go test` time instead of only discovering it live, mid-faulttest-run.
//
// Added after two manual catalog-wide series_id renames in one sitting
// (pbs_db_pitr_recovery -> pbs_db_data_loss_triage,
// pbs_db_config_recovery -> pbs_db_config_triage) touched 30+ files by hand
// with no automated check that every reference was actually caught — a
// typo in any of them would otherwise have surfaced only as a confusing
// live-test failure ("playbook not found" or a silent
// requires_operator_approval coercion), not a test failure.
func TestCatalogPlaybookReferencesExist(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}
	all, err := ps.List(ctx, audit.PlaybookListQuery{IncludeSystem: true, ActiveOnly: false})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seriesExists := make(map[string]bool, len(all))
	for _, pb := range all {
		seriesExists[pb.SeriesID] = true
	}

	// A stale transitions_to/escalates_to target is coerced server-side to
	// requires_operator_approval rather than actually chaining (see
	// audit.Playbook.TransitionsTo's own doc comment) — a silent behavior
	// change, not an error, so it needs this explicit check to catch.
	for _, pb := range all {
		for _, target := range pb.TransitionsTo {
			if !seriesExists[target] {
				t.Errorf("%s: transitions_to references %q, which is not a seeded playbook series_id", pb.SeriesID, target)
			}
		}
		for _, target := range pb.EscalatesTo {
			if !seriesExists[target] {
				t.Errorf("%s: escalates_to references %q, which is not a seeded playbook series_id", pb.SeriesID, target)
			}
		}
	}

	catalogPath := findCatalogFromPlaybooksDir()
	if catalogPath == "" {
		t.Skip("could not find testing/catalog/failures.yaml")
	}
	catalog, err := faultlib.LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	for _, f := range catalog.Failures {
		if f.DiagnosisPlaybookSeriesID != "" && !seriesExists[f.DiagnosisPlaybookSeriesID] {
			t.Errorf("fault %q: diagnosis_playbook_series_id %q is not a seeded playbook series_id", f.ID, f.DiagnosisPlaybookSeriesID)
		}
		if f.Remediation.PlaybookID != "" && !seriesExists[f.Remediation.PlaybookID] {
			t.Errorf("fault %q: remediation.playbook_id %q is not a seeded playbook series_id", f.ID, f.Remediation.PlaybookID)
		}
	}
}

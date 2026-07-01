package playbooks_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/playbooks"
)

// newTestStore returns a PlaybookStore backed by a fresh temp-dir SQLite DB.
func newTestStore(t *testing.T) *audit.PlaybookStore {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ps, err := audit.NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	return ps
}

func TestSeedSystemPlaybooks_FirstVersionActive(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()

	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}

	all, err := ps.List(ctx, audit.PlaybookListQuery{
		ActiveOnly:    false,
		IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("expected at least one seeded playbook")
	}
	for _, pb := range all {
		if !pb.IsSystem {
			t.Errorf("playbook %q: IsSystem=false, want true", pb.Name)
		}
		if pb.Source != "system" {
			t.Errorf("playbook %q: Source=%q, want system", pb.Name, pb.Source)
		}
		if !pb.IsActive {
			t.Errorf("playbook %q (series %q): IsActive=false; first-seeded version should be active", pb.Name, pb.SeriesID)
		}
	}
}

func TestSeedSystemPlaybooks_Idempotent(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()

	// Seed twice.
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	all, err := ps.List(ctx, audit.PlaybookListQuery{
		ActiveOnly:    false,
		IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Count distinct series — each should appear exactly once.
	seen := map[string]int{}
	for _, pb := range all {
		seen[pb.SeriesID]++
	}
	for seriesID, count := range seen {
		if count != 1 {
			t.Errorf("series %q: found %d rows after idempotent seed, want 1", seriesID, count)
		}
	}
}

func TestSeedSystemPlaybooks_NewVersionIsInactive(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()

	// Seed the shipped playbooks first.
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Pick any series that was seeded.
	all, err := ps.List(ctx, audit.PlaybookListQuery{
		ActiveOnly:    false,
		IncludeSystem: true,
	})
	if err != nil || len(all) == 0 {
		t.Fatalf("need at least one seeded playbook; err=%v", err)
	}
	original := all[0]

	// Manually insert a second version into the same series (simulates a future
	// update to the embedded YAML where the version string changes).
	v2 := &audit.Playbook{
		SeriesID:     original.SeriesID,
		Name:         original.Name + " v2",
		Version:      "99.0",
		ProblemClass: original.ProblemClass,
		Description:  "updated description",
		IsSystem:     true,
		IsActive:     false,
		Source:       "system",
	}
	if err := ps.Create(ctx, v2); err != nil {
		t.Fatalf("Create v2: %v", err)
	}

	// Seed again — v2 already exists, v1 was already seeded; nothing changes.
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	// The series should have exactly 2 rows.
	series, err := ps.List(ctx, audit.PlaybookListQuery{
		SeriesID:      original.SeriesID,
		ActiveOnly:    false,
		IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("List series: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 versions in series, got %d", len(series))
	}

	// Exactly one should be active (the original v1 since we never called Activate).
	activeCount := 0
	for _, pb := range series {
		if pb.IsActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("expected 1 active version in series, got %d", activeCount)
	}
}

// TestSeedSystemPlaybooks_DoesNotClobberActivatedGeneratedVersion verifies that
// when an operator has promoted a generated playbook version in a series, a
// subsequent auditd restart (which re-runs the seeder) does not roll the series
// back to the system baseline.
func TestSeedSystemPlaybooks_DoesNotClobberActivatedGeneratedVersion(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()

	// Seed shipped system playbooks.
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("initial seed: %v", err)
	}

	// Find any series that was seeded (active system version).
	all, err := ps.List(ctx, audit.PlaybookListQuery{ActiveOnly: true, IncludeSystem: true})
	if err != nil || len(all) == 0 {
		t.Fatalf("need at least one seeded playbook; err=%v len=%d", err, len(all))
	}
	systemPB := all[0]

	// Simulate the operator generating and activating v_next (a generated version).
	generated := &audit.Playbook{
		SeriesID:     systemPB.SeriesID,
		Name:         systemPB.Name + " (generated)",
		Version:      "99.0",
		ProblemClass: systemPB.ProblemClass,
		Description:  "operator-improved version",
		IsSystem:     false,
		IsActive:     false,
		Source:       "generated",
	}
	if err := ps.Create(ctx, generated); err != nil {
		t.Fatalf("Create generated version: %v", err)
	}
	// Activate the generated version — this deactivates the system version.
	if err := ps.Activate(ctx, generated.PlaybookID); err != nil {
		t.Fatalf("Activate generated version: %v", err)
	}

	// Confirm system version is now inactive.
	reloaded, err := ps.List(ctx, audit.PlaybookListQuery{
		SeriesID: systemPB.SeriesID, ActiveOnly: false, IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("List after activate: %v", err)
	}
	for _, pb := range reloaded {
		if pb.PlaybookID == systemPB.PlaybookID && pb.IsActive {
			t.Fatal("system version should be inactive after activating generated version")
		}
	}

	// Re-run the seeder (simulates auditd restart).
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	// The generated version must still be active; the system version must stay inactive.
	afterReseed, err := ps.List(ctx, audit.PlaybookListQuery{
		SeriesID: systemPB.SeriesID, ActiveOnly: false, IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("List after re-seed: %v", err)
	}
	activeCount := 0
	for _, pb := range afterReseed {
		if pb.IsActive {
			activeCount++
			if pb.PlaybookID != generated.PlaybookID {
				t.Errorf("wrong version is active after re-seed: got %s (source=%s, version=%s), want generated %s",
					pb.PlaybookID, pb.Source, pb.Version, generated.PlaybookID)
			}
		}
	}
	if activeCount != 1 {
		t.Errorf("expected exactly 1 active version after re-seed, got %d", activeCount)
	}
}

func TestSeedSystemPlaybooks_YAMLParseRoundtrip(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()

	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}

	all, err := ps.List(ctx, audit.PlaybookListQuery{
		ActiveOnly:    false,
		IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for _, pb := range all {
		if pb.Name == "" {
			t.Errorf("series %q: empty name after seed", pb.SeriesID)
		}
		if pb.Description == "" {
			t.Errorf("series %q: empty description after seed", pb.SeriesID)
		}
		if pb.Guidance == "" {
			t.Errorf("series %q: empty guidance after seed", pb.SeriesID)
		}
		if len(pb.Symptoms) == 0 {
			t.Errorf("series %q: no symptoms after seed", pb.SeriesID)
		}
		if len(pb.Escalation) == 0 {
			t.Errorf("series %q: no escalation criteria after seed", pb.SeriesID)
		}
	}
}

func TestSeedSystemPlaybooks_NewFields(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ps, err := audit.NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}

	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}

	all, err := ps.List(ctx, audit.PlaybookListQuery{IncludeSystem: true, ActiveOnly: false})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	bySeriesID := make(map[string]*audit.Playbook)
	for _, pb := range all {
		bySeriesID[pb.SeriesID] = pb
	}

	// Restart triage: entry_point=true, execution_mode=agent, escalates_to set.
	restart := bySeriesID["pbs_db_restart_triage"]
	if restart == nil {
		t.Fatal("pbs_db_restart_triage not seeded")
	}
	if !restart.EntryPoint {
		t.Error("pbs_db_restart_triage: entry_point = false, want true")
	}
	if restart.ExecutionMode != "agent" {
		t.Errorf("pbs_db_restart_triage: execution_mode = %q, want agent", restart.ExecutionMode)
	}
	if len(restart.EscalatesTo) == 0 {
		t.Error("pbs_db_restart_triage: escalates_to is empty")
	}

	// Config recovery: execution_mode=agent, transitions_to and requires_evidence set.
	config := bySeriesID["pbs_db_config_recovery"]
	if config == nil {
		t.Fatal("pbs_db_config_recovery not seeded")
	}
	if config.ExecutionMode != "agent" {
		t.Errorf("pbs_db_config_recovery: execution_mode = %q, want agent", config.ExecutionMode)
	}
	if len(config.TransitionsTo) == 0 {
		t.Error("pbs_db_config_recovery: transitions_to is empty")
	}
	if len(config.RequiresEvidence) == 0 {
		t.Error("pbs_db_config_recovery: requires_evidence is empty")
	}

	// PITR recovery: execution_mode=agent, requires_evidence set, entry_point=false.
	pitr := bySeriesID["pbs_db_pitr_recovery"]
	if pitr == nil {
		t.Fatal("pbs_db_pitr_recovery not seeded")
	}
	if pitr.ExecutionMode != "agent" {
		t.Errorf("pbs_db_pitr_recovery: execution_mode = %q, want agent", pitr.ExecutionMode)
	}
	if len(pitr.RequiresEvidence) == 0 {
		t.Error("pbs_db_pitr_recovery: requires_evidence is empty")
	}
	if pitr.EntryPoint {
		t.Error("pbs_db_pitr_recovery: entry_point = true, want false")
	}

	// Triage playbooks: execution_mode=agent (converted from fleet to return text responses).
	for _, sid := range []string{"pbs_vacuum_triage", "pbs_slow_query_triage", "pbs_connection_triage", "pbs_replication_lag"} {
		pb := bySeriesID[sid]
		if pb == nil {
			t.Errorf("%s not seeded", sid)
			continue
		}
		if pb.ExecutionMode != "agent" {
			t.Errorf("%s: execution_mode = %q, want agent", sid, pb.ExecutionMode)
		}
	}
}

// TestPlaybookStructure_NoReplaceMeStrings checks that no system playbook still
// contains unfilled template placeholders from the triage/remediation templates.
func TestPlaybookStructure_NoReplaceMeStrings(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}
	all, err := ps.List(ctx, audit.PlaybookListQuery{ActiveOnly: false, IncludeSystem: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, pb := range all {
		for _, f := range []struct{ name, val string }{
			{"name", pb.Name},
			{"description", pb.Description},
			{"guidance", pb.Guidance},
		} {
			if strings.Contains(f.val, "REPLACE_ME") || strings.Contains(f.val, "REPLACE ME") {
				t.Errorf("series %q: field %q contains unfilled template placeholder", pb.SeriesID, f.name)
			}
		}
	}
}

// TestPlaybookStructure_TriageSignalLine checks that every entry-point triage
// playbook (execution_mode=agent + entry_point=true) instructs the agent to
// emit either TRANSITION_TO: or ESCALATE_TO: in its guidance. Without a signal
// line the gateway cannot chain the triage to a remediation or close the run.
func TestPlaybookStructure_TriageSignalLine(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}
	all, err := ps.List(ctx, audit.PlaybookListQuery{ActiveOnly: false, IncludeSystem: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, pb := range all {
		if !pb.EntryPoint || pb.ExecutionMode != "agent" {
			continue
		}
		hasTransition := strings.Contains(pb.Guidance, "TRANSITION_TO:")
		hasEscalation := strings.Contains(pb.Guidance, "ESCALATE_TO:")
		if !hasTransition && !hasEscalation {
			t.Errorf("series %q (entry_point=true, execution_mode=agent): guidance is missing a TRANSITION_TO: or ESCALATE_TO: signal line — gateway cannot chain this run", pb.SeriesID)
		}
		if hasTransition && hasEscalation {
			// Both can appear in the guidance for conditional logic (e.g. different
			// escalation paths). That is intentional — only flag it as a log entry.
			t.Logf("series %q: guidance references both TRANSITION_TO and ESCALATE_TO (conditional branching)", pb.SeriesID)
		}
	}
}

// TestPlaybookStructure_TransitionTargetsExist verifies that every TRANSITION_TO:
// reference in any playbook's guidance points to a series_id that actually exists
// in the vault. A typo here breaks the triage→remediation chain silently.
func TestPlaybookStructure_TransitionTargetsExist(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}
	all, err := ps.List(ctx, audit.PlaybookListQuery{ActiveOnly: false, IncludeSystem: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	known := make(map[string]bool, len(all))
	for _, pb := range all {
		known[pb.SeriesID] = true
	}
	for _, pb := range all {
		for _, line := range strings.Split(pb.Guidance, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "TRANSITION_TO:") {
				continue
			}
			target := strings.TrimSpace(strings.TrimPrefix(line, "TRANSITION_TO:"))
			if target == "" {
				t.Errorf("series %q: TRANSITION_TO: line has empty target", pb.SeriesID)
				continue
			}
			if !known[target] {
				t.Errorf("series %q: TRANSITION_TO: %q — no playbook with that series_id exists in the vault", pb.SeriesID, target)
			}
		}
	}
}

// TestPlaybookStructure_HypothesisFormat checks that every entry-point triage
// playbook (execution_mode=agent, entry_point=true) instructs the agent to emit
// HYPOTHESIS_1: lines. Without this, parseDiagnosticReport never populates and
// lowConfidenceForceGate cannot enforce the 50% confidence gate.
func TestPlaybookStructure_HypothesisFormat(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}
	all, err := ps.List(ctx, audit.PlaybookListQuery{ActiveOnly: false, IncludeSystem: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, pb := range all {
		if !pb.EntryPoint || pb.ExecutionMode != "agent" {
			continue
		}
		if !strings.Contains(pb.Guidance, "HYPOTHESIS_1:") {
			t.Errorf("series %q (entry_point=true, execution_mode=agent): guidance missing HYPOTHESIS_1: — agent will not emit structured diagnostic report", pb.SeriesID)
		}
		if !strings.Contains(pb.Guidance, "ROOT_CAUSE:") {
			t.Errorf("series %q: guidance missing ROOT_CAUSE: line — parseDiagnosticReport will not mark a primary hypothesis", pb.SeriesID)
		}
	}
}

// TestPlaybookStructure_SeriesIDConvention checks that every system playbook
// follows the pbs_ series_id naming convention required by the gateway.
func TestPlaybookStructure_SeriesIDConvention(t *testing.T) {
	ps := newTestStore(t)
	ctx := context.Background()
	if err := playbooks.SeedSystemPlaybooks(ctx, ps); err != nil {
		t.Fatalf("SeedSystemPlaybooks: %v", err)
	}
	all, err := ps.List(ctx, audit.PlaybookListQuery{ActiveOnly: false, IncludeSystem: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, pb := range all {
		if !strings.HasPrefix(pb.SeriesID, "pbs_") {
			t.Errorf("series_id %q does not follow pbs_ naming convention", pb.SeriesID)
		}
	}
}

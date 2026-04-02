package playbooks_test

import (
	"context"
	"path/filepath"
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

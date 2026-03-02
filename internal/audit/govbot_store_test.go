package audit

import (
	"path/filepath"
	"testing"
	"time"
)

func newGovbotTestStore(t *testing.T) *GovbotStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	gs, err := NewGovbotStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewGovbotStore: %v", err)
	}
	return gs
}

func makeRun(gateway, status string) GovbotRun {
	return GovbotRun{
		RunAt:   time.Now().UTC(),
		Window:  "24h",
		Gateway: gateway,
		Status:  status,
	}
}

func TestGovbotStore_SaveAndRecentRuns(t *testing.T) {
	gs := newGovbotTestStore(t)

	for i := 0; i < 3; i++ {
		r := makeRun("http://gw:8080", "healthy")
		r.RunAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if err := gs.SaveRun(r); err != nil {
			t.Fatalf("SaveRun %d: %v", i, err)
		}
	}

	runs, err := gs.RecentRuns("24h", "http://gw:8080", 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	// Newest first
	for i := 1; i < len(runs); i++ {
		if runs[i].RunAt.After(runs[i-1].RunAt) {
			t.Errorf("runs not sorted newest-first at index %d", i)
		}
	}
}

func TestGovbotStore_Prune(t *testing.T) {
	gs := newGovbotTestStore(t)

	const total = 10
	const retain = 4
	gw := "http://gw:8080"

	for i := 0; i < total; i++ {
		r := makeRun(gw, "healthy")
		r.RunAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if err := gs.SaveRun(r); err != nil {
			t.Fatalf("SaveRun %d: %v", i, err)
		}
	}

	if err := gs.Prune(gw, retain); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	runs, err := gs.RecentRuns("24h", gw, 100)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != retain {
		t.Fatalf("expected %d runs after prune, got %d", retain, len(runs))
	}
}

func TestGovbotStore_Prune_IsolatesGateway(t *testing.T) {
	gs := newGovbotTestStore(t)

	gw1 := "http://gw1:8080"
	gw2 := "http://gw2:8080"

	// 5 runs for gw1, 3 for gw2
	for i := 0; i < 5; i++ {
		gs.SaveRun(makeRun(gw1, "healthy")) //nolint:errcheck
	}
	for i := 0; i < 3; i++ {
		gs.SaveRun(makeRun(gw2, "healthy")) //nolint:errcheck
	}

	// Prune gw1 to 2; gw2 must be untouched
	if err := gs.Prune(gw1, 2); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	gw1Runs, _ := gs.RecentRuns("", gw1, 100)
	gw2Runs, _ := gs.RecentRuns("", gw2, 100)

	if len(gw1Runs) != 2 {
		t.Errorf("gw1: expected 2 after prune, got %d", len(gw1Runs))
	}
	if len(gw2Runs) != 3 {
		t.Errorf("gw2: expected 3 untouched, got %d", len(gw2Runs))
	}
}

func TestGovbotStore_Prune_Noop(t *testing.T) {
	gs := newGovbotTestStore(t)
	gw := "http://gw:8080"
	gs.SaveRun(makeRun(gw, "healthy")) //nolint:errcheck

	// retain=0 → no-op
	if err := gs.Prune(gw, 0); err != nil {
		t.Fatalf("Prune(retain=0): %v", err)
	}
	// empty gateway → no-op
	if err := gs.Prune("", 5); err != nil {
		t.Fatalf("Prune(gateway=''): %v", err)
	}

	runs, _ := gs.RecentRuns("", gw, 100)
	if len(runs) != 1 {
		t.Errorf("expected 1 run unchanged, got %d", len(runs))
	}
}

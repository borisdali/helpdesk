package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "govbot-test.db")
}

func makeSnap(status string, window string, denies, muts int, chainValid bool) runSnapshot {
	return runSnapshot{
		RunAt:                time.Now().UTC(),
		Window:               window,
		Gateway:              "http://gateway:8080",
		Status:               status,
		AlertCount:           0,
		WarningCount:         0,
		AlertsJSON:           "[]",
		WarningsJSON:         "[]",
		ChainValid:           chainValid,
		PolicyDenies:         denies,
		MutationsTotal:       muts,
		MutationsDestructive: 0,
		PendingApprovals:     0,
		StaleApprovals:       0,
		DecisionsByResource:  "{}",
	}
}

// TestHistory_OpenCreate verifies that openHistory creates the schema on a
// fresh database and can be re-opened without error.
func TestHistory_OpenCreate(t *testing.T) {
	path := tempDB(t)

	h, err := openHistory(path)
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	h.close()

	// Re-open — schema creation must be idempotent.
	h2, err := openHistory(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	h2.close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

// TestHistory_SaveAndQuery saves a handful of snapshots and verifies that
// recent() returns them newest-first and honours the window filter.
func TestHistory_SaveAndQuery(t *testing.T) {
	h, err := openHistory(tempDB(t))
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	defer h.close()

	// Insert 3 "24h" runs and 1 "7d" run.
	for i := 0; i < 3; i++ {
		s := makeSnap("healthy", "24h", i, i*2, true)
		s.RunAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if err := h.save(s, 0); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	wkSnap := makeSnap("warnings", "7d", 5, 10, true)
	if err := h.save(wkSnap, 0); err != nil {
		t.Fatalf("save 7d: %v", err)
	}

	// recent with window filter
	snaps, err := h.recent("24h", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("expected 3 snaps for 24h window, got %d", len(snaps))
	}
	// Newest first
	for i := 1; i < len(snaps); i++ {
		if snaps[i].RunAt.After(snaps[i-1].RunAt) {
			t.Errorf("snaps not sorted newest-first at index %d", i)
		}
	}

	// recent without filter sees all 4
	all, err := h.recent("", 10)
	if err != nil {
		t.Fatalf("recent all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 total snaps, got %d", len(all))
	}
}

// TestHistory_Retention saves more rows than the retain limit and verifies
// the oldest are pruned.
func TestHistory_Retention(t *testing.T) {
	h, err := openHistory(tempDB(t))
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	defer h.close()

	const retain = 5
	for i := 0; i < 10; i++ {
		s := makeSnap("healthy", "24h", 0, 0, true)
		s.RunAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if err := h.save(s, retain); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	snaps, err := h.recent("24h", 100)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(snaps) != retain {
		t.Fatalf("expected %d rows after pruning, got %d", retain, len(snaps))
	}
}

// TestHistory_TrendCalc verifies that the average/delta arithmetic used in
// the trend block produces the expected results.
func TestHistory_TrendCalc(t *testing.T) {
	h, err := openHistory(tempDB(t))
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	defer h.close()

	// Save 5 known runs: denies = 2,4,6,8,10; muts = 10,20,30,40,50
	for i := 1; i <= 5; i++ {
		s := makeSnap("healthy", "24h", i*2, i*10, true)
		s.RunAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if err := h.save(s, 0); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	prior, err := h.recent("24h", 6)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(prior) != 5 {
		t.Fatalf("expected 5 prior, got %d", len(prior))
	}

	// prior[0] is the newest (denies=10, muts=50), treat it as "today".
	// prior[1:] = 4 runs with denies {8,6,4,2}, muts {40,30,20,10}.
	prev := prior[1:]
	totalDenies, totalMuts := 0, 0
	for _, p := range prev {
		totalDenies += p.PolicyDenies
		totalMuts += p.MutationsTotal
	}
	n := len(prev) // 4
	avgDenies := float64(totalDenies) / float64(n)
	avgMuts := float64(totalMuts) / float64(n)

	// avg denies = (8+6+4+2)/4 = 5.0
	if avgDenies != 5.0 {
		t.Errorf("avgDenies = %f, want 5.0", avgDenies)
	}
	// avg muts = (40+30+20+10)/4 = 25.0
	if avgMuts != 25.0 {
		t.Errorf("avgMuts = %f, want 25.0", avgMuts)
	}

	// "today" run (prior[0]) has denies=10, muts=50.
	// Both are > 1.5× avg → should flag as above avg.
	today := prior[0]
	if float64(today.PolicyDenies) <= avgDenies*1.5 {
		t.Errorf("expected denies %d > 1.5 × avg %.1f", today.PolicyDenies, avgDenies)
	}
	if float64(today.MutationsTotal) <= avgMuts*1.5 {
		t.Errorf("expected muts %d > 1.5 × avg %.1f", today.MutationsTotal, avgMuts)
	}
}

// TestHistory_InvocationsByResource verifies that the InvocationsByResource
// field round-trips correctly through save and recent.
func TestHistory_InvocationsByResource(t *testing.T) {
	h, err := openHistory(tempDB(t))
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	defer h.close()

	s := makeSnap("healthy", "24h", 0, 0, true)
	s.InvocationsByResource = `{"database/prod-db:write":{"invoked":5,"checked":3}}`

	if err := h.save(s, 0); err != nil {
		t.Fatalf("save: %v", err)
	}

	snaps, err := h.recent("24h", 1)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snap, got %d", len(snaps))
	}
	if snaps[0].InvocationsByResource != s.InvocationsByResource {
		t.Errorf("InvocationsByResource = %q, want %q",
			snaps[0].InvocationsByResource, s.InvocationsByResource)
	}
}

// TestHistory_PrintTable verifies that printTable runs without error on a
// populated database (basic smoke test for the table rendering path).
func TestHistory_PrintTable(t *testing.T) {
	h, err := openHistory(tempDB(t))
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	defer h.close()

	for i := 0; i < 3; i++ {
		s := makeSnap("healthy", "24h", i, i*3, i%2 == 0)
		s.RunAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if err := h.save(s, 0); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// Capture stdout (just verify no panic/error — output goes to os.Stdout).
	if err := h.printTable("24h", 10); err != nil {
		t.Fatalf("printTable: %v", err)
	}
	// Empty window with no matching rows.
	if err := h.printTable("7d", 10); err != nil {
		t.Fatalf("printTable empty: %v", err)
	}
}

// TestHistory_RemoteClient verifies that remoteHistoryClient round-trips a
// snapshot through a fake HTTP server that mimics the auditd govbot endpoints.
func TestHistory_RemoteClient(t *testing.T) {
	var saved []audit.GovbotRun

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/govbot/runs", func(w http.ResponseWriter, r *http.Request) {
		var run audit.GovbotRun
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved = append(saved, run)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /v1/govbot/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved) //nolint:errcheck
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	rc := openRemoteHistory(srv.URL, "http://gateway:8080")

	snap := makeSnap("healthy", "24h", 3, 15, true)
	if err := rc.save(snap, 0); err != nil {
		t.Fatalf("save: %v", err)
	}

	snaps, err := rc.recent("24h", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snap, got %d", len(snaps))
	}
	if snaps[0].PolicyDenies != 3 {
		t.Errorf("PolicyDenies: got %d, want 3", snaps[0].PolicyDenies)
	}
	if snaps[0].MutationsTotal != 15 {
		t.Errorf("MutationsTotal: got %d, want 15", snaps[0].MutationsTotal)
	}
	if snaps[0].Status != "healthy" {
		t.Errorf("Status: got %q, want %q", snaps[0].Status, "healthy")
	}

	// printTable should not error on the remote client.
	if err := rc.printTable("24h", 10); err != nil {
		t.Fatalf("printTable: %v", err)
	}
}

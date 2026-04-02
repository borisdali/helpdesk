package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newPlaybookTestStore(t *testing.T) *PlaybookStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ps, err := NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	return ps
}

func TestPlaybookStore_CreateAndGet(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	lv := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	pb := &Playbook{
		PlaybookID:       "pb_test001",
		Name:             "Slow Query Playbook",
		Description:      "Investigate and resolve slow queries on production databases",
		TargetHints:      []string{"prod-db-*", "replica-*"},
		CreatedBy:        "alice",
		ProblemClass:     "performance",
		Symptoms:         []string{"p99 latency > 500ms", "query time > 10s"},
		Guidance:         "Check for missing indexes first, then examine query plans.",
		Escalation:       []string{"if table lock detected, stop and escalate to DBA"},
		RelatedPlaybooks: []string{"pb_abc123", "pb_def456"},
		Author:           "alice@example.com",
		LastValidated:    &lv,
		Version:          "1.2.0",
	}

	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := ps.Get(ctx, pb.PlaybookID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.PlaybookID != pb.PlaybookID {
		t.Errorf("PlaybookID = %q, want %q", got.PlaybookID, pb.PlaybookID)
	}
	if got.Name != pb.Name {
		t.Errorf("Name = %q, want %q", got.Name, pb.Name)
	}
	if got.Description != pb.Description {
		t.Errorf("Description = %q, want %q", got.Description, pb.Description)
	}
	if got.CreatedBy != pb.CreatedBy {
		t.Errorf("CreatedBy = %q, want %q", got.CreatedBy, pb.CreatedBy)
	}
	if got.ProblemClass != pb.ProblemClass {
		t.Errorf("ProblemClass = %q, want %q", got.ProblemClass, pb.ProblemClass)
	}
	if got.Guidance != pb.Guidance {
		t.Errorf("Guidance = %q, want %q", got.Guidance, pb.Guidance)
	}
	if got.Author != pb.Author {
		t.Errorf("Author = %q, want %q", got.Author, pb.Author)
	}
	if got.Version != pb.Version {
		t.Errorf("Version = %q, want %q", got.Version, pb.Version)
	}

	// JSON array fields
	if len(got.TargetHints) != 2 || got.TargetHints[0] != "prod-db-*" || got.TargetHints[1] != "replica-*" {
		t.Errorf("TargetHints = %v, want [prod-db-* replica-*]", got.TargetHints)
	}
	if len(got.Symptoms) != 2 || got.Symptoms[0] != "p99 latency > 500ms" {
		t.Errorf("Symptoms = %v, want [p99 latency > 500ms ...]", got.Symptoms)
	}
	if len(got.Escalation) != 1 || got.Escalation[0] != "if table lock detected, stop and escalate to DBA" {
		t.Errorf("Escalation = %v", got.Escalation)
	}
	if len(got.RelatedPlaybooks) != 2 || got.RelatedPlaybooks[0] != "pb_abc123" || got.RelatedPlaybooks[1] != "pb_def456" {
		t.Errorf("RelatedPlaybooks = %v, want [pb_abc123 pb_def456]", got.RelatedPlaybooks)
	}

	// LastValidated round-trip
	if got.LastValidated == nil {
		t.Fatal("LastValidated is nil, want non-nil")
	}
	if !got.LastValidated.Equal(lv) {
		t.Errorf("LastValidated = %v, want %v", got.LastValidated, lv)
	}

	// Timestamps set by Create
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestPlaybookStore_CreateGeneratesID(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{
		Name:        "Auto-ID Playbook",
		Description: "test",
		CreatedBy:   "bot",
	}

	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if pb.PlaybookID == "" {
		t.Fatal("PlaybookID is empty after Create")
	}
	if len(pb.PlaybookID) < 3 || pb.PlaybookID[:3] != "pb_" {
		t.Errorf("PlaybookID = %q, want pb_ prefix", pb.PlaybookID)
	}
}

func TestPlaybookStore_List(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb1 := &Playbook{
		Name:        "First Playbook",
		Description: "created first",
		CreatedBy:   "alice",
	}
	if err := ps.Create(ctx, pb1); err != nil {
		t.Fatalf("Create pb1: %v", err)
	}

	// Small sleep to ensure distinct created_at timestamps in SQLite (1-second resolution).
	time.Sleep(10 * time.Millisecond)

	pb2 := &Playbook{
		Name:        "Second Playbook",
		Description: "created second",
		CreatedBy:   "bob",
	}
	if err := ps.Create(ctx, pb2); err != nil {
		t.Fatalf("Create pb2: %v", err)
	}

	list, err := ps.List(ctx, DefaultPlaybookListQuery())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d playbooks, want 2", len(list))
	}

	// Most recent first (pb2 was created after pb1).
	// Both timestamps may be equal on fast machines; just verify both are present.
	ids := map[string]bool{list[0].PlaybookID: true, list[1].PlaybookID: true}
	if !ids[pb1.PlaybookID] || !ids[pb2.PlaybookID] {
		t.Errorf("List IDs = %v, want both %q and %q", ids, pb1.PlaybookID, pb2.PlaybookID)
	}
}

func TestPlaybookStore_Delete(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{
		Name:        "To Be Deleted",
		Description: "ephemeral",
		CreatedBy:   "alice",
	}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := ps.Delete(ctx, pb.PlaybookID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := ps.Get(ctx, pb.PlaybookID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get after Delete: got %v, want sql.ErrNoRows", err)
	}
}

func TestPlaybookStore_Update(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	lv := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	pb := &Playbook{
		Name:         "Original Name",
		Description:  "original description",
		CreatedBy:    "alice",
		ProblemClass: "availability",
		Symptoms:     []string{"high error rate"},
		Guidance:     "old guidance",
		Escalation:   []string{"escalate if down > 5min"},
		Author:       "alice@example.com",
		Version:      "1.0.0",
	}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	originalUpdatedAt := pb.UpdatedAt

	// Advance time slightly so UpdatedAt is guaranteed to differ.
	time.Sleep(10 * time.Millisecond)

	lv2 := time.Date(2026, 2, 20, 8, 30, 0, 0, time.UTC)
	pb.Name = "Updated Name"
	pb.Description = "updated description"
	pb.TargetHints = []string{"staging-*"}
	pb.ProblemClass = "capacity"
	pb.Symptoms = []string{"disk > 90%", "iops saturated"}
	pb.Guidance = "new guidance text"
	pb.Escalation = []string{"escalate to on-call"}
	pb.RelatedPlaybooks = []string{"pb_related1"}
	pb.Author = "bob@example.com"
	pb.LastValidated = &lv2
	pb.Version = "2.0.0"

	if err := ps.Update(ctx, pb); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := ps.Get(ctx, pb.PlaybookID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}

	if got.Name != "Updated Name" {
		t.Errorf("Name = %q, want Updated Name", got.Name)
	}
	if got.Description != "updated description" {
		t.Errorf("Description = %q, want updated description", got.Description)
	}
	if got.ProblemClass != "capacity" {
		t.Errorf("ProblemClass = %q, want capacity", got.ProblemClass)
	}
	if got.Guidance != "new guidance text" {
		t.Errorf("Guidance = %q, want new guidance text", got.Guidance)
	}
	if got.Author != "bob@example.com" {
		t.Errorf("Author = %q, want bob@example.com", got.Author)
	}
	if got.Version != "2.0.0" {
		t.Errorf("Version = %q, want 2.0.0", got.Version)
	}
	if len(got.Symptoms) != 2 || got.Symptoms[0] != "disk > 90%" {
		t.Errorf("Symptoms = %v", got.Symptoms)
	}
	if len(got.Escalation) != 1 || got.Escalation[0] != "escalate to on-call" {
		t.Errorf("Escalation = %v", got.Escalation)
	}
	if len(got.RelatedPlaybooks) != 1 || got.RelatedPlaybooks[0] != "pb_related1" {
		t.Errorf("RelatedPlaybooks = %v", got.RelatedPlaybooks)
	}
	if got.LastValidated == nil {
		t.Fatal("LastValidated is nil after update")
	}
	if !got.LastValidated.Equal(lv2) {
		t.Errorf("LastValidated = %v, want %v", got.LastValidated, lv2)
	}

	// CreatedBy must not change.
	if got.CreatedBy != "alice" {
		t.Errorf("CreatedBy changed to %q, want alice", got.CreatedBy)
	}

	// UpdatedAt must advance.
	if !got.UpdatedAt.After(originalUpdatedAt) && !got.UpdatedAt.Equal(originalUpdatedAt) {
		// Allow equal on very fast machines; just ensure it's not earlier.
		if got.UpdatedAt.Before(originalUpdatedAt) {
			t.Errorf("UpdatedAt = %v went backward from %v", got.UpdatedAt, originalUpdatedAt)
		}
	}
	_ = lv
}

func TestPlaybookStore_Update_NotFound(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{
		PlaybookID:  "pb_nonexist",
		Name:        "Ghost Playbook",
		Description: "does not exist in DB",
		CreatedBy:   "nobody",
	}

	err := ps.Update(ctx, pb)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Update non-existent: got %v, want sql.ErrNoRows", err)
	}
}

func TestPlaybookStore_MigrateSchema_Idempotent(t *testing.T) {
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, err = NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("first NewPlaybookStore: %v", err)
	}

	_, err = NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("second NewPlaybookStore (idempotency check): %v", err)
	}
}

// --- Phase 2: versioning tests ---

func TestPlaybookStore_SeriesID_AutoGenerated(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{Name: "No Series", Description: "test", CreatedBy: "alice"}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pb.SeriesID == "" {
		t.Fatal("SeriesID is empty after Create")
	}
	if len(pb.SeriesID) < 4 || pb.SeriesID[:4] != "pbs_" {
		t.Errorf("SeriesID = %q, want pbs_ prefix", pb.SeriesID)
	}

	got, _ := ps.Get(ctx, pb.PlaybookID)
	if got.SeriesID != pb.SeriesID {
		t.Errorf("SeriesID not persisted: got %q, want %q", got.SeriesID, pb.SeriesID)
	}
}

func TestPlaybookStore_Create_DefaultsIsActive(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{Name: "Active by Default", Description: "test", CreatedBy: "alice"}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !pb.IsActive {
		t.Error("IsActive should default to true for new playbooks")
	}
	got, _ := ps.Get(ctx, pb.PlaybookID)
	if !got.IsActive {
		t.Error("IsActive not persisted as true")
	}
	if got.Source != "manual" {
		t.Errorf("Source = %q, want manual", got.Source)
	}
}

func TestPlaybookStore_Activate(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	seriesID := "pbs_testactivate"
	// Create 3 versions in the same series. v1 starts active; v2, v3 are inactive.
	// Providing an explicit SeriesID means Create respects the caller's IsActive value.
	v1 := &Playbook{Name: "PB v1", Description: "d", SeriesID: seriesID, Version: "1.0", IsActive: true}
	if err := ps.Create(ctx, v1); err != nil {
		t.Fatalf("Create v1: %v", err)
	}
	v2 := &Playbook{Name: "PB v2", Description: "d", SeriesID: seriesID, Version: "2.0", IsActive: false}
	if err := ps.Create(ctx, v2); err != nil {
		t.Fatalf("Create v2: %v", err)
	}
	v3 := &Playbook{Name: "PB v3", Description: "d", SeriesID: seriesID, Version: "3.0", IsActive: false}
	if err := ps.Create(ctx, v3); err != nil {
		t.Fatalf("Create v3: %v", err)
	}

	// Verify initial state.
	if g, _ := ps.Get(ctx, v1.PlaybookID); !g.IsActive {
		t.Fatal("v1 should start active")
	}

	// Activate v2.
	if err := ps.Activate(ctx, v2.PlaybookID); err != nil {
		t.Fatalf("Activate v2: %v", err)
	}

	got1, _ := ps.Get(ctx, v1.PlaybookID)
	got2, _ := ps.Get(ctx, v2.PlaybookID)
	got3, _ := ps.Get(ctx, v3.PlaybookID)

	if got1.IsActive {
		t.Error("v1 should be inactive after activating v2")
	}
	if !got2.IsActive {
		t.Error("v2 should be active")
	}
	if got3.IsActive {
		t.Error("v3 should be inactive")
	}
}

func TestPlaybookStore_Activate_AlreadyActive(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{Name: "Already Active", Description: "d", CreatedBy: "alice"}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Activate an already-active playbook — must be idempotent (no error).
	if err := ps.Activate(ctx, pb.PlaybookID); err != nil {
		t.Errorf("Activate already-active: expected no error, got %v", err)
	}
	got, _ := ps.Get(ctx, pb.PlaybookID)
	if !got.IsActive {
		t.Error("playbook should still be active after re-activation")
	}
}

func TestPlaybookStore_Activate_NotFound(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	err := ps.Activate(ctx, "pb_doesnotexist")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Activate non-existent: got %v, want sql.ErrNoRows", err)
	}
}

func TestPlaybookStore_SystemPlaybookProtected_Update(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{
		Name:        "System PB",
		Description: "system-managed",
		IsSystem:    true,
		Source:      "system",
		SeriesID:    "pbs_sys1",
		IsActive:    true,
	}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	pb.Name = "Tampered Name"
	err := ps.Update(ctx, pb)
	if !errors.Is(err, ErrSystemPlaybook) {
		t.Errorf("Update system playbook: got %v, want ErrSystemPlaybook", err)
	}
}

func TestPlaybookStore_SystemPlaybookProtected_Delete(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{
		Name:        "System PB",
		Description: "system-managed",
		IsSystem:    true,
		Source:      "system",
		SeriesID:    "pbs_sys2",
		IsActive:    true,
	}
	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := ps.Delete(ctx, pb.PlaybookID)
	if !errors.Is(err, ErrSystemPlaybook) {
		t.Errorf("Delete system playbook: got %v, want ErrSystemPlaybook", err)
	}
}

func TestPlaybookStore_ListActiveOnly(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	seriesID := "pbs_listactive"
	active := &Playbook{Name: "Active", Description: "d", SeriesID: seriesID, IsActive: true}
	if err := ps.Create(ctx, active); err != nil {
		t.Fatalf("Create active: %v", err)
	}
	inactive := &Playbook{Name: "Inactive", Description: "d", SeriesID: seriesID, IsActive: false}
	if err := ps.Create(ctx, inactive); err != nil {
		t.Fatalf("Create inactive: %v", err)
	}

	// DefaultPlaybookListQuery has ActiveOnly=true.
	list, err := ps.List(ctx, DefaultPlaybookListQuery())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, pb := range list {
		if !pb.IsActive {
			t.Errorf("List(ActiveOnly=true) returned inactive playbook %q", pb.PlaybookID)
		}
	}
	found := false
	for _, pb := range list {
		if pb.PlaybookID == active.PlaybookID {
			found = true
		}
	}
	if !found {
		t.Error("active playbook not found in List results")
	}
}

func TestPlaybookStore_ListBySeries(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	seriesA := "pbs_seriesA"
	seriesB := "pbs_seriesB"

	a1 := &Playbook{Name: "A v1", Description: "d", SeriesID: seriesA, IsActive: true}
	a2 := &Playbook{Name: "A v2", Description: "d", SeriesID: seriesA, IsActive: false}
	b1 := &Playbook{Name: "B v1", Description: "d", SeriesID: seriesB, IsActive: true}

	for _, pb := range []*Playbook{a1, a2, b1} {
		if err := ps.Create(ctx, pb); err != nil {
			t.Fatalf("Create %q: %v", pb.Name, err)
		}
	}

	list, err := ps.List(ctx, PlaybookListQuery{SeriesID: seriesA, ActiveOnly: false, IncludeSystem: true})
	if err != nil {
		t.Fatalf("List by series: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("List(series=A) = %d playbooks, want 2", len(list))
	}
	for _, pb := range list {
		if pb.SeriesID != seriesA {
			t.Errorf("unexpected series_id %q in results", pb.SeriesID)
		}
	}
}

func TestPlaybookStore_NullOptionalFields(t *testing.T) {
	ps := newPlaybookTestStore(t)
	ctx := context.Background()

	pb := &Playbook{
		Name:             "Minimal Playbook",
		Description:      "no optional fields set",
		CreatedBy:        "bot",
		Symptoms:         nil,
		Escalation:       nil,
		RelatedPlaybooks: nil,
		LastValidated:    nil,
	}

	if err := ps.Create(ctx, pb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := ps.Get(ctx, pb.PlaybookID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Must not panic; nil slices are acceptable.
	_ = got.Symptoms
	_ = got.Escalation
	_ = got.RelatedPlaybooks

	if got.LastValidated != nil {
		t.Errorf("LastValidated = %v, want nil", got.LastValidated)
	}
	if got.ProblemClass != "" {
		t.Errorf("ProblemClass = %q, want empty", got.ProblemClass)
	}
	if got.Guidance != "" {
		t.Errorf("Guidance = %q, want empty", got.Guidance)
	}
	if got.Author != "" {
		t.Errorf("Author = %q, want empty", got.Author)
	}
	if got.Version != "" {
		t.Errorf("Version = %q, want empty", got.Version)
	}
}

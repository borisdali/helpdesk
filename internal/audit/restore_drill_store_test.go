package audit

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newDrillTestStore(t *testing.T) *RestoreDrillStore {
	t.Helper()
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ds, err := NewRestoreDrillStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewRestoreDrillStore: %v", err)
	}
	return ds
}

func goodDrill(source string, finished time.Time) DrillRecord {
	return DrillRecord{
		SourceKey: source, CloneKey: source + "-clone",
		SourceSystemID: "7001", CloneSystemID: "7001",
		Method: DrillMethodPgBackRest, Status: DrillVerified,
		StartedAt: finished.Add(-90 * time.Second), FinishedAt: finished,
		DurationSeconds: 90, DurationSource: DrillDurationReported,
		RestoreBytes: 1 << 30,
		Checks:       []DrillCheck{{Name: "same_system", Result: "pass"}},
	}
}

func TestRestoreDrillStore_SaveGetRoundTrip(t *testing.T) {
	ds := newDrillTestStore(t)
	fin := time.Now().UTC().Truncate(time.Second)
	in := goodDrill("prod-db", fin)
	in.Ticket, in.Note, in.RecordedBy, in.BackupLabel = "INC-9", "quarterly", "alice", "20261101-020000F"
	saved, err := ds.Save(in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.HasPrefix(saved.DrillID, "drl_") || saved.RecordedAt.IsZero() {
		t.Fatalf("id/recorded_at not assigned: %+v", saved)
	}
	got, err := ds.Get(saved.DrillID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SourceKey != "prod-db" || got.Ticket != "INC-9" || got.BackupLabel != "20261101-020000F" ||
		got.DurationSeconds != 90 || got.RestoreBytes != 1<<30 || !got.FinishedAt.Equal(fin) ||
		len(got.Checks) != 1 || got.Checks[0].Name != "same_system" {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestRestoreDrillStore_RecordsAreImmutable(t *testing.T) {
	ds := newDrillTestStore(t)
	d := goodDrill("prod-db", time.Now())
	d.DrillID = "drl_fixed01"
	if _, err := ds.Save(d); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Save(d); err == nil {
		t.Fatal("saving the same drill ID twice must fail, records are immutable")
	}
}

func TestDrillRecord_Validate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		edit func(*DrillRecord)
		want string // substring of the error, "" for valid
	}{
		{"valid", func(*DrillRecord) {}, ""},
		{"no source", func(d *DrillRecord) { d.SourceKey = "" }, "source_key"},
		{"no clone", func(d *DrillRecord) { d.CloneKey = "" }, "clone_key"},
		{"clone is source", func(d *DrillRecord) { d.CloneKey = d.SourceKey }, "cannot be the source"},
		{"bad method", func(d *DrillRecord) { d.Method = "dump" }, "method"},
		{"bad status", func(d *DrillRecord) { d.Status = "great" }, "status"},
		{"negative duration", func(d *DrillRecord) { d.DurationSeconds = -1 }, "negative"},
		{"bad duration source", func(d *DrillRecord) { d.DurationSource = "guessed" }, "duration_source"},
		{"finished before started", func(d *DrillRecord) { d.FinishedAt = d.StartedAt.Add(-time.Hour) }, "before started_at"},
		{"verified without ids", func(d *DrillRecord) { d.CloneSystemID = "" }, "system identifiers"},
		{"verified with different ids", func(d *DrillRecord) { d.CloneSystemID = "9" }, "same system identifier"},
		{"warnings with different ids", func(d *DrillRecord) { d.Status = DrillVerifiedWithWarnings; d.CloneSystemID = "9" }, "same system identifier"},
		{"refused may have different ids", func(d *DrillRecord) { d.Status = DrillRefused; d.CloneSystemID = "9" }, ""},
		{"unverifiable may have no ids", func(d *DrillRecord) { d.Status = DrillUnverifiable; d.SourceSystemID, d.CloneSystemID = "", "" }, ""},
	}
	for _, tc := range cases {
		d := goodDrill("prod-db", now)
		tc.edit(&d)
		err := d.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestRestoreDrillStore_ListFiltersAndOrder(t *testing.T) {
	ds := newDrillTestStore(t)
	now := time.Now().UTC()
	old := goodDrill("prod-db", now.Add(-100*24*time.Hour))
	recent := goodDrill("prod-db", now.Add(-2*24*time.Hour))
	other := goodDrill("other-db", now.Add(-time.Hour))
	other.SourceSystemID, other.CloneSystemID = "8002", "8002"
	refused := goodDrill("prod-db", now.Add(-time.Hour))
	refused.Status = DrillRefused
	snap := goodDrill("prod-db", now.Add(-3*time.Hour))
	snap.Method = DrillMethodSnapshot
	for _, d := range []DrillRecord{old, recent, other, refused, snap} {
		if _, err := ds.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	list := func(q DrillQuery) []DrillRecord {
		t.Helper()
		out, err := ds.List(q)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := list(DrillQuery{SourceKey: "prod-db"}); len(got) != 4 {
		t.Fatalf("source_key filter: %d rows, want 4", len(got))
	} else if !got[0].FinishedAt.After(got[1].FinishedAt) {
		t.Errorf("not newest first: %v then %v", got[0].FinishedAt, got[1].FinishedAt)
	}
	if got := list(DrillQuery{SourceSystemID: "8002"}); len(got) != 1 || got[0].SourceKey != "other-db" {
		t.Errorf("system id filter: %+v", got)
	}
	if got := list(DrillQuery{SourceKey: "prod-db", Since: now.Add(-90 * 24 * time.Hour)}); len(got) != 3 {
		t.Errorf("90 day window dropped the wrong rows: %d, want 3", len(got))
	}
	if got := list(DrillQuery{SourceKey: "prod-db", Status: DrillRefused}); len(got) != 1 {
		t.Errorf("status filter: %d", len(got))
	}
	if got := list(DrillQuery{SourceKey: "prod-db", Method: DrillMethodSnapshot}); len(got) != 1 {
		t.Errorf("method filter: %d", len(got))
	}
	if got := list(DrillQuery{SourceKey: "prod-db", Limit: 2}); len(got) != 2 {
		t.Errorf("limit: %d", len(got))
	}
}

func TestRestoreDrillStore_Supersedes(t *testing.T) {
	ds := newDrillTestStore(t)
	now := time.Now().UTC()
	first := goodDrill("prod-db", now.Add(-time.Hour))
	first.Status = DrillRefused
	first, err := ds.Save(first)
	if err != nil {
		t.Fatal(err)
	}
	again := goodDrill("prod-db", now)
	again.Supersedes = first.DrillID
	again, err = ds.Save(again)
	if err != nil {
		t.Fatalf("re-verification: %v", err)
	}
	all, _ := ds.List(DrillQuery{SourceKey: "prod-db"})
	cur := Current(all)
	if len(all) != 2 || len(cur) != 1 || cur[0].DrillID != again.DrillID {
		t.Errorf("Current kept the wrong rows: all=%d current=%+v", len(all), cur)
	}

	bad := goodDrill("prod-db", now)
	bad.Supersedes = "drl_nope"
	if _, err := ds.Save(bad); err == nil {
		t.Error("superseding an unknown drill must fail")
	}
	cross := goodDrill("other-db", now)
	cross.Supersedes = first.DrillID
	if _, err := ds.Save(cross); err == nil {
		t.Error("superseding a drill of another source must fail")
	}
}

func TestRestoreDrillStore_GetUnknown(t *testing.T) {
	ds := newDrillTestStore(t)
	if _, err := ds.Get("drl_missing"); !errors.Is(err, ErrDrillNotFound) {
		t.Errorf("got %v, want ErrDrillNotFound", err)
	}
}

func TestRestoreDrillStore_SchemaIsIdempotent(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 2; i++ {
		if _, err := NewRestoreDrillStore(store.DB(), store.IsPostgres()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
}

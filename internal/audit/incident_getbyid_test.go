package audit

import (
	"context"
	"testing"
	"time"
)

// An incident is read back by its ID whether it is open (no resolved_at) or
// resolved: the gateway resolves an inc_ ID to its entry run through this.
func TestIncidentStore_GetByID_OpenAndResolved(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	open := &Incident{IncidentID: "inc_open1", EntryRunID: "plr_open1", TraceID: "tr_open1", SeriesID: "pbs_x", DetectedAt: now}
	done := &Incident{IncidentID: "inc_done1", EntryRunID: "plr_done1", TraceID: "tr_done1", SeriesID: "pbs_x", DetectedAt: now,
		Status: IncidentStatusResolved, ResolvedAt: now.Add(time.Minute)}
	for _, inc := range []*Incident{open, done} {
		if err := s.Create(ctx, inc); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []*Incident{open, done} {
		got, err := s.GetByID(ctx, want.IncidentID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", want.IncidentID, err)
		}
		if got.EntryRunID != want.EntryRunID || got.Status != want.Status && want.Status != "" {
			t.Errorf("GetByID(%s) = %+v", want.IncidentID, got)
		}
	}
}

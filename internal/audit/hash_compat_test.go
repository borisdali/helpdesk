package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// legacyHash is ComputeEventHash exactly as it was before the restore_drill
// payload was added to the hash input. An audit trail written by an older
// binary must verify under the current one, so every event that carries no
// drill has to hash to the same value as this. Do not "update" this function
// when the hash input changes again: a change that makes this test fail would
// make existing trails fail verification.
func legacyHash(event *Event) string {
	in := struct {
		EventID          string            `json:"event_id"`
		Timestamp        string            `json:"timestamp"`
		EventType        EventType         `json:"event_type"`
		TraceID          string            `json:"trace_id,omitempty"`
		ParentID         string            `json:"parent_id,omitempty"`
		ActionClass      ActionClass       `json:"action_class,omitempty"`
		PrevHash         string            `json:"prev_hash,omitempty"`
		Session          Session           `json:"session"`
		Input            Input             `json:"input"`
		Output           *Output           `json:"output,omitempty"`
		Tool             *ToolExecution    `json:"tool,omitempty"`
		Approval         *Approval         `json:"approval,omitempty"`
		Decision         *Decision         `json:"decision,omitempty"`
		Outcome          *Outcome          `json:"outcome,omitempty"`
		RecoveryDecision *RecoveryDecision `json:"recovery_decision,omitempty"`
	}{
		event.EventID, event.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"), event.EventType,
		event.TraceID, event.ParentID, event.ActionClass, event.PrevHash, event.Session, event.Input,
		event.Output, event.Tool, event.Approval, event.Decision, event.Outcome, event.RecoveryDecision,
	}
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestComputeEventHash_UnchangedForEventsWithoutADrill(t *testing.T) {
	ts := time.Date(2026, 11, 1, 12, 0, 0, 123456789, time.UTC)
	events := []*Event{
		{EventID: "evt_1", Timestamp: ts, EventType: EventTypeDelegation, PrevHash: GenesisHash,
			Session: Session{ID: "s", UserID: "u"}, Input: Input{UserQuery: "q"}},
		{EventID: "evt_2", Timestamp: ts, EventType: EventTypeToolExecution, TraceID: "tr_1", ActionClass: ActionDestructive,
			Tool: &ToolExecution{Name: "restore_to_point_in_time"}, Outcome: &Outcome{Status: "success"}},
		NewRecoveryDecisionEvent("plr_1", "tr_1", RecoveryDecision{
			Stage: RecoveryStageGate, Decision: RecoveryDecisionApproved, RunID: "plr_1",
			Approval: &ApprovalFacts{ApprovedBy: "alice", Ticket: "INC-1"}}),
	}
	events[2].Timestamp = ts
	for _, e := range events {
		if got, want := ComputeEventHash(e), legacyHash(e); got != want {
			t.Errorf("event %s (%s): hash %s, legacy %s: existing audit trails would stop verifying", e.EventID, e.EventType, got, want)
		}
	}
}

func TestComputeEventHash_CoversTheDrill(t *testing.T) {
	ts := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	mk := func(status string) *Event {
		return &Event{EventID: "evt_d", Timestamp: ts, EventType: EventTypeRestoreDrill,
			RestoreDrill: &DrillRecord{DrillID: "drl_1", SourceKey: "a", CloneKey: "b", Method: DrillMethodPgBackRest, Status: status,
				SourceSystemID: "1", CloneSystemID: "1", RecordedAt: ts}}
	}
	if ComputeEventHash(mk(DrillVerified)) == ComputeEventHash(mk(DrillRefused)) {
		t.Error("the drill's verdict is not part of the hash")
	}
	if ComputeEventHash(mk(DrillVerified)) == legacyHash(mk(DrillVerified)) {
		t.Error("a drill event hashes like one without a drill")
	}
}

// A chain that mixes ordinary events and drills verifies through the store,
// and a stored drill that is edited afterwards is caught.
func TestStoreChain_VerifiesWithDrillEventsInIt(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	rec := func(e *Event) {
		t.Helper()
		if err := store.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rec(&Event{EventType: EventTypeDelegation, Session: Session{ID: "s"}, Input: Input{UserQuery: "one"}})
	d := goodDrill("prod-db", time.Now().UTC())
	rec(&Event{EventType: EventTypeRestoreDrill, TraceID: "drl_x", RestoreDrill: &d})
	rec(&Event{EventType: EventTypeDelegation, Session: Session{ID: "s"}, Input: Input{UserQuery: "three"}})

	st, err := store.VerifyIntegrity(ctx)
	if err != nil || !st.Valid || st.TotalEvents != 3 {
		t.Fatalf("chain with a drill in it: %+v (%v)", st, err)
	}

	// Edit the stored drill behind the store's back: the raw JSON is what verification reads.
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE audit_events SET raw_json = REPLACE(raw_json, '"status":"verified"', '"status":"refused"') WHERE event_type = 'restore_drill'`); err != nil {
		t.Fatal(err)
	}
	st, err = store.VerifyIntegrity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Valid {
		t.Error("editing a recorded drill did not break the chain")
	}
}

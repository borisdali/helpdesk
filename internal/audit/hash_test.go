package audit

import (
	"testing"
	"time"
)

func TestComputeEventHash(t *testing.T) {
	event := &Event{
		EventID:   "evt_test123",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  GenesisHash,
		Session: Session{
			ID:     "sess_abc",
			UserID: "testuser",
		},
		Input: Input{
			UserQuery: "test query",
		},
	}

	hash := ComputeEventHash(event)

	// Hash should be deterministic
	if hash == "" {
		t.Error("hash should not be empty")
	}
	if len(hash) != 64 { // SHA-256 produces 64 hex characters
		t.Errorf("hash length = %d, want 64", len(hash))
	}

	// Same event should produce same hash
	hash2 := ComputeEventHash(event)
	if hash != hash2 {
		t.Errorf("hash not deterministic: %s != %s", hash, hash2)
	}

	// Different event should produce different hash
	event2 := *event
	event2.EventID = "evt_different"
	hash3 := ComputeEventHash(&event2)
	if hash == hash3 {
		t.Error("different events should produce different hashes")
	}
}

func TestVerifyEventHash(t *testing.T) {
	event := &Event{
		EventID:   "evt_verify",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  GenesisHash,
		Session: Session{
			ID:     "sess_verify",
			UserID: "verifyuser",
		},
		Input: Input{
			UserQuery: "verify test",
		},
	}

	// Compute and set the hash
	event.EventHash = ComputeEventHash(event)

	// Should verify as valid
	if !VerifyEventHash(event) {
		t.Error("event with correct hash should verify")
	}

	// Tamper with the event
	event.Input.UserQuery = "tampered query"
	if VerifyEventHash(event) {
		t.Error("tampered event should not verify")
	}
}

func TestVerifyEventHash_EmptyHash(t *testing.T) {
	event := &Event{
		EventID:   "evt_legacy",
		EventHash: "", // Legacy event without hash
	}

	// Empty hash should be considered valid (legacy compatibility)
	if !VerifyEventHash(event) {
		t.Error("event without hash should be considered valid (legacy)")
	}
}

func TestVerifyChain(t *testing.T) {
	// Create a valid chain of events
	event1 := &Event{
		EventID:   "evt_001",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  GenesisHash,
		Session:   Session{ID: "sess_chain"},
		Input:     Input{UserQuery: "first"},
	}
	event1.EventHash = ComputeEventHash(event1)

	event2 := &Event{
		EventID:   "evt_002",
		Timestamp: time.Date(2025, 1, 1, 12, 1, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  event1.EventHash,
		Session:   Session{ID: "sess_chain"},
		Input:     Input{UserQuery: "second"},
	}
	event2.EventHash = ComputeEventHash(event2)

	event3 := &Event{
		EventID:   "evt_003",
		Timestamp: time.Date(2025, 1, 1, 12, 2, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  event2.EventHash,
		Session:   Session{ID: "sess_chain"},
		Input:     Input{UserQuery: "third"},
	}
	event3.EventHash = ComputeEventHash(event3)

	events := []Event{*event1, *event2, *event3}

	// Valid chain should verify
	brokenAt, err := VerifyChain(events)
	if err != nil {
		t.Errorf("valid chain should verify: %v", err)
	}
	if brokenAt != -1 {
		t.Errorf("brokenAt = %d, want -1", brokenAt)
	}
}

func TestVerifyChain_Broken(t *testing.T) {
	// Create events with broken chain
	event1 := &Event{
		EventID:   "evt_001",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  GenesisHash,
		Session:   Session{ID: "sess_broken"},
		Input:     Input{UserQuery: "first"},
	}
	event1.EventHash = ComputeEventHash(event1)

	event2 := &Event{
		EventID:   "evt_002",
		Timestamp: time.Date(2025, 1, 1, 12, 1, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  "wrong_hash_0000000000000000000000000000000000000000000000", // Wrong!
		Session:   Session{ID: "sess_broken"},
		Input:     Input{UserQuery: "second"},
	}
	event2.EventHash = ComputeEventHash(event2)

	events := []Event{*event1, *event2}

	// Broken chain should be detected
	brokenAt, err := VerifyChain(events)
	if err == nil {
		t.Error("broken chain should return error")
	}
	if brokenAt != 1 {
		t.Errorf("brokenAt = %d, want 1", brokenAt)
	}
}

func TestVerifyChain_Empty(t *testing.T) {
	events := []Event{}

	brokenAt, err := VerifyChain(events)
	if err != nil {
		t.Errorf("empty chain should verify: %v", err)
	}
	if brokenAt != -1 {
		t.Errorf("brokenAt = %d, want -1", brokenAt)
	}
}

func TestVerifyChain_InvalidEventHash(t *testing.T) {
	event := &Event{
		EventID:   "evt_tampered",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  GenesisHash,
		Session:   Session{ID: "sess_tampered"},
		Input:     Input{UserQuery: "original"},
	}
	event.EventHash = ComputeEventHash(event)

	// Now tamper with content after hash was computed
	event.Input.UserQuery = "tampered"

	events := []Event{*event}

	brokenAt, err := VerifyChain(events)
	if err == nil {
		t.Error("chain with tampered event should return error")
	}
	if brokenAt != 0 {
		t.Errorf("brokenAt = %d, want 0", brokenAt)
	}
}

func TestVerifyChainStatus(t *testing.T) {
	// Create a valid chain
	event1 := &Event{
		EventID:   "evt_status1",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  GenesisHash,
		Session:   Session{ID: "sess_status"},
		Input:     Input{UserQuery: "first"},
	}
	event1.EventHash = ComputeEventHash(event1)

	event2 := &Event{
		EventID:   "evt_status2",
		Timestamp: time.Date(2025, 1, 1, 12, 1, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  event1.EventHash,
		Session:   Session{ID: "sess_status"},
		Input:     Input{UserQuery: "second"},
	}
	event2.EventHash = ComputeEventHash(event2)

	events := []Event{*event1, *event2}

	status := VerifyChainStatus(events)

	if !status.Valid {
		t.Error("status.Valid should be true")
	}
	if status.TotalEvents != 2 {
		t.Errorf("TotalEvents = %d, want 2", status.TotalEvents)
	}
	if status.HashedEvents != 2 {
		t.Errorf("HashedEvents = %d, want 2", status.HashedEvents)
	}
	if status.LegacyEvents != 0 {
		t.Errorf("LegacyEvents = %d, want 0", status.LegacyEvents)
	}
	if status.FirstEventID != "evt_status1" {
		t.Errorf("FirstEventID = %q, want %q", status.FirstEventID, "evt_status1")
	}
	if status.LastEventID != "evt_status2" {
		t.Errorf("LastEventID = %q, want %q", status.LastEventID, "evt_status2")
	}
	if status.LastHash != event2.EventHash {
		t.Errorf("LastHash = %q, want %q", status.LastHash, event2.EventHash)
	}
	if status.BrokenAt != -1 {
		t.Errorf("BrokenAt = %d, want -1", status.BrokenAt)
	}
}

func TestVerifyChainStatus_MixedLegacy(t *testing.T) {
	// Mix of legacy (no hash) and hashed events
	legacyEvent := &Event{
		EventID:   "evt_legacy",
		Timestamp: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		// No PrevHash or EventHash
		Session: Session{ID: "sess_mixed"},
		Input:   Input{UserQuery: "legacy"},
	}

	hashedEvent := &Event{
		EventID:   "evt_hashed",
		Timestamp: time.Date(2025, 1, 1, 12, 1, 0, 0, time.UTC),
		EventType: EventTypeDelegation,
		PrevHash:  ComputeEventHash(legacyEvent), // Links to computed hash of legacy
		Session:   Session{ID: "sess_mixed"},
		Input:     Input{UserQuery: "hashed"},
	}
	hashedEvent.EventHash = ComputeEventHash(hashedEvent)

	events := []Event{*legacyEvent, *hashedEvent}

	status := VerifyChainStatus(events)

	if !status.Valid {
		t.Errorf("mixed chain should be valid: %s", status.Error)
	}
	if status.LegacyEvents != 1 {
		t.Errorf("LegacyEvents = %d, want 1", status.LegacyEvents)
	}
	if status.HashedEvents != 1 {
		t.Errorf("HashedEvents = %d, want 1", status.HashedEvents)
	}
}

func TestGenesisHash(t *testing.T) {
	// Genesis hash should be all zeros
	expected := "0000000000000000000000000000000000000000000000000000000000000000"
	if GenesisHash != expected {
		t.Errorf("GenesisHash = %q, want %q", GenesisHash, expected)
	}
	if len(GenesisHash) != 64 {
		t.Errorf("GenesisHash length = %d, want 64", len(GenesisHash))
	}
}

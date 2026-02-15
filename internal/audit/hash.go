package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// HashAlgorithm identifies the hashing algorithm used.
const HashAlgorithm = "sha256"

// GenesisHash is the hash used for the first event in the chain.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// ComputeEventHash computes the hash of an event.
// The hash is computed over the canonical JSON representation of the event,
// excluding the EventHash field itself (to avoid circular dependency).
func ComputeEventHash(event *Event) string {
	// Create a copy without the hash field for consistent hashing
	hashInput := struct {
		EventID     string      `json:"event_id"`
		Timestamp   string      `json:"timestamp"`
		EventType   EventType   `json:"event_type"`
		TraceID     string      `json:"trace_id,omitempty"`
		ParentID    string      `json:"parent_id,omitempty"`
		ActionClass ActionClass `json:"action_class,omitempty"`
		PrevHash    string      `json:"prev_hash,omitempty"`
		Session     Session     `json:"session"`
		Input       Input       `json:"input"`
		Output      *Output     `json:"output,omitempty"`
		Tool        *ToolExecution `json:"tool,omitempty"`
		Approval    *Approval   `json:"approval,omitempty"`
		Decision    *Decision   `json:"decision,omitempty"`
		Outcome     *Outcome    `json:"outcome,omitempty"`
	}{
		EventID:     event.EventID,
		Timestamp:   event.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
		EventType:   event.EventType,
		TraceID:     event.TraceID,
		ParentID:    event.ParentID,
		ActionClass: event.ActionClass,
		PrevHash:    event.PrevHash,
		Session:     event.Session,
		Input:       event.Input,
		Output:      event.Output,
		Tool:        event.Tool,
		Approval:    event.Approval,
		Decision:    event.Decision,
		Outcome:     event.Outcome,
	}

	data, err := json.Marshal(hashInput)
	if err != nil {
		// Fallback to event ID if marshaling fails
		data = []byte(event.EventID)
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// VerifyEventHash verifies that an event's hash is correct.
func VerifyEventHash(event *Event) bool {
	if event.EventHash == "" {
		return true // No hash to verify (legacy event)
	}
	computed := ComputeEventHash(event)
	return computed == event.EventHash
}

// VerifyChain verifies the integrity of a chain of events.
// Events must be in chronological order.
// Returns the index of the first broken link, or -1 if chain is valid.
func VerifyChain(events []Event) (int, error) {
	if len(events) == 0 {
		return -1, nil
	}

	for i, event := range events {
		// Verify event's own hash
		if event.EventHash != "" && !VerifyEventHash(&event) {
			return i, fmt.Errorf("event %s has invalid hash", event.EventID)
		}

		// Verify chain link (PrevHash matches previous event's hash)
		if i > 0 {
			prevEvent := events[i-1]
			expectedPrevHash := prevEvent.EventHash
			if expectedPrevHash == "" {
				// Compute hash for legacy events
				expectedPrevHash = ComputeEventHash(&prevEvent)
			}

			if event.PrevHash != "" && event.PrevHash != expectedPrevHash {
				return i, fmt.Errorf("event %s has broken chain link: prev_hash=%s, expected=%s",
					event.EventID, event.PrevHash[:16]+"...", expectedPrevHash[:16]+"...")
			}
		} else {
			// First event should have genesis hash or empty
			if event.PrevHash != "" && event.PrevHash != GenesisHash {
				return i, fmt.Errorf("first event %s has invalid prev_hash (expected genesis or empty)",
					event.EventID)
			}
		}
	}

	return -1, nil
}

// ChainStatus represents the integrity status of the audit chain.
type ChainStatus struct {
	Valid        bool   `json:"valid"`
	TotalEvents  int    `json:"total_events"`
	HashedEvents int    `json:"hashed_events"` // Events with hash chains
	LegacyEvents int    `json:"legacy_events"` // Events without hashes
	BrokenAt     int    `json:"broken_at,omitempty"` // Index of first break (-1 if valid)
	Error        string `json:"error,omitempty"`
	FirstEventID string `json:"first_event_id,omitempty"`
	LastEventID  string `json:"last_event_id,omitempty"`
	LastHash     string `json:"last_hash,omitempty"`
}

// VerifyChainStatus performs a full chain verification and returns status.
func VerifyChainStatus(events []Event) ChainStatus {
	status := ChainStatus{
		TotalEvents: len(events),
		BrokenAt:    -1,
	}

	if len(events) == 0 {
		status.Valid = true
		return status
	}

	status.FirstEventID = events[0].EventID
	status.LastEventID = events[len(events)-1].EventID

	// Count hashed vs legacy events
	for _, e := range events {
		if e.EventHash != "" {
			status.HashedEvents++
		} else {
			status.LegacyEvents++
		}
	}

	// Get last hash
	lastEvent := events[len(events)-1]
	if lastEvent.EventHash != "" {
		status.LastHash = lastEvent.EventHash
	} else {
		status.LastHash = ComputeEventHash(&lastEvent)
	}

	// Verify chain
	brokenAt, err := VerifyChain(events)
	if err != nil {
		status.Valid = false
		status.BrokenAt = brokenAt
		status.Error = err.Error()
	} else {
		status.Valid = true
	}

	return status
}

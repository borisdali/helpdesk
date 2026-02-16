package audit

import "context"

// Auditor is the interface for recording audit events.
// Implemented by both Store (local SQLite) and RemoteStore (HTTP client).
type Auditor interface {
	// Record persists an audit event.
	Record(ctx context.Context, event *Event) error

	// RecordOutcome updates an event with its final outcome.
	RecordOutcome(ctx context.Context, eventID string, outcome *Outcome) error

	// Query retrieves events matching the given options.
	Query(ctx context.Context, opts QueryOptions) ([]Event, error)

	// Close releases any resources held by the auditor.
	Close() error
}

// Ensure implementations satisfy the interface.
var (
	_ Auditor = (*Store)(nil)
	_ Auditor = (*RemoteStore)(nil)
)

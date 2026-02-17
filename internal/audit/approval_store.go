package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ApprovalStore persists approval requests to SQLite.
type ApprovalStore struct {
	db       *sql.DB
	mu       sync.RWMutex
	waiters  map[string][]chan *StoredApproval // keyed by approval_id
	waiterMu sync.Mutex
}

// StoredApproval represents a pending or resolved approval request.
type StoredApproval struct {
	// Identifiers
	ApprovalID string `json:"approval_id"`
	EventID    string `json:"event_id,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`

	// Status
	Status string `json:"status"` // pending, approved, denied, expired, cancelled

	// Request details
	ActionClass  string `json:"action_class"`
	ToolName     string `json:"tool_name,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`

	// Principal
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`

	// Context (JSON blob)
	RequestContext map[string]any `json:"request_context,omitempty"`

	// Resolution
	ResolvedBy       string    `json:"resolved_by,omitempty"`
	ResolvedAt       time.Time `json:"resolved_at,omitempty"`
	ResolutionReason string    `json:"resolution_reason,omitempty"`

	// Expiration
	ExpiresAt         time.Time `json:"expires_at,omitempty"`
	ApprovalValidUntil time.Time `json:"approval_valid_until,omitempty"`

	// Policy
	PolicyName   string `json:"policy_name,omitempty"`
	ApproverRole string `json:"approver_role,omitempty"`

	// Callback
	CallbackURL    string    `json:"callback_url,omitempty"`
	CallbackSentAt time.Time `json:"callback_sent_at,omitempty"`

	// Timestamps
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewApprovalStore creates a new approval store using the given database connection.
// The database should already be opened (typically shared with the audit Store).
func NewApprovalStore(db *sql.DB) (*ApprovalStore, error) {
	if err := createApprovalTables(db); err != nil {
		return nil, fmt.Errorf("create approval tables: %w", err)
	}

	return &ApprovalStore{
		db:      db,
		waiters: make(map[string][]chan *StoredApproval),
	}, nil
}

func createApprovalTables(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS approval_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		approval_id TEXT UNIQUE NOT NULL,
		event_id TEXT,
		trace_id TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		action_class TEXT NOT NULL,
		tool_name TEXT,
		agent_name TEXT,
		resource_type TEXT,
		resource_name TEXT,
		requested_by TEXT NOT NULL,
		requested_at TEXT NOT NULL,
		request_context TEXT,
		resolved_by TEXT,
		resolved_at TEXT,
		resolution_reason TEXT,
		expires_at TEXT,
		approval_valid_until TEXT,
		policy_name TEXT,
		approver_role TEXT,
		callback_url TEXT,
		callback_sent_at TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// Create indexes
	indexes := `
	CREATE INDEX IF NOT EXISTS idx_approvals_status ON approval_requests(status);
	CREATE INDEX IF NOT EXISTS idx_approvals_trace ON approval_requests(trace_id);
	CREATE INDEX IF NOT EXISTS idx_approvals_event ON approval_requests(event_id);
	CREATE INDEX IF NOT EXISTS idx_approvals_requested_by ON approval_requests(requested_by);
	CREATE INDEX IF NOT EXISTS idx_approvals_expires ON approval_requests(expires_at);
	CREATE INDEX IF NOT EXISTS idx_approvals_agent ON approval_requests(agent_name);
	CREATE INDEX IF NOT EXISTS idx_approvals_tool ON approval_requests(tool_name);
	`
	_, err := db.Exec(indexes)
	return err
}

// CreateRequest creates a new approval request.
func (s *ApprovalStore) CreateRequest(ctx context.Context, req *StoredApproval) error {
	if req.ApprovalID == "" {
		req.ApprovalID = "apr_" + uuid.New().String()[:8]
	}
	if req.RequestedAt.IsZero() {
		req.RequestedAt = time.Now().UTC()
	}
	if req.Status == "" {
		req.Status = "pending"
	}
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = req.CreatedAt

	contextJSON, err := json.Marshal(req.RequestContext)
	if err != nil {
		contextJSON = []byte("{}")
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO approval_requests (
			approval_id, event_id, trace_id, status,
			action_class, tool_name, agent_name, resource_type, resource_name,
			requested_by, requested_at, request_context,
			expires_at, policy_name, approver_role, callback_url,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		req.ApprovalID,
		req.EventID,
		req.TraceID,
		req.Status,
		req.ActionClass,
		req.ToolName,
		req.AgentName,
		req.ResourceType,
		req.ResourceName,
		req.RequestedBy,
		req.RequestedAt.Format(time.RFC3339Nano),
		string(contextJSON),
		formatTimeOrNull(req.ExpiresAt),
		req.PolicyName,
		req.ApproverRole,
		req.CallbackURL,
		req.CreatedAt.Format(time.RFC3339Nano),
		req.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetRequest retrieves an approval request by ID.
func (s *ApprovalStore) GetRequest(ctx context.Context, approvalID string) (*StoredApproval, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT approval_id, event_id, trace_id, status,
			action_class, tool_name, agent_name, resource_type, resource_name,
			requested_by, requested_at, request_context,
			resolved_by, resolved_at, resolution_reason,
			expires_at, approval_valid_until, policy_name, approver_role,
			callback_url, callback_sent_at, created_at, updated_at
		FROM approval_requests WHERE approval_id = ?
	`, approvalID)

	return scanStoredApproval(row)
}

// GetRequestByTraceAndTool finds an approval for a specific trace and tool.
func (s *ApprovalStore) GetRequestByTraceAndTool(ctx context.Context, traceID, toolName string) (*StoredApproval, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT approval_id, event_id, trace_id, status,
			action_class, tool_name, agent_name, resource_type, resource_name,
			requested_by, requested_at, request_context,
			resolved_by, resolved_at, resolution_reason,
			expires_at, approval_valid_until, policy_name, approver_role,
			callback_url, callback_sent_at, created_at, updated_at
		FROM approval_requests
		WHERE trace_id = ? AND tool_name = ?
		ORDER BY created_at DESC LIMIT 1
	`, traceID, toolName)

	return scanStoredApproval(row)
}

// ApprovalQueryOptions specifies filters for listing approvals.
type ApprovalQueryOptions struct {
	Status      string
	AgentName   string
	TraceID     string
	RequestedBy string
	Since       time.Time
	Limit       int
}

// ListRequests returns approval requests matching the filters.
func (s *ApprovalStore) ListRequests(ctx context.Context, opts ApprovalQueryOptions) ([]*StoredApproval, error) {
	query := `
		SELECT approval_id, event_id, trace_id, status,
			action_class, tool_name, agent_name, resource_type, resource_name,
			requested_by, requested_at, request_context,
			resolved_by, resolved_at, resolution_reason,
			expires_at, approval_valid_until, policy_name, approver_role,
			callback_url, callback_sent_at, created_at, updated_at
		FROM approval_requests WHERE 1=1
	`
	var args []any

	if opts.Status != "" {
		query += " AND status = ?"
		args = append(args, opts.Status)
	}
	if opts.AgentName != "" {
		query += " AND agent_name = ?"
		args = append(args, opts.AgentName)
	}
	if opts.TraceID != "" {
		query += " AND trace_id = ?"
		args = append(args, opts.TraceID)
	}
	if opts.RequestedBy != "" {
		query += " AND requested_by = ?"
		args = append(args, opts.RequestedBy)
	}
	if !opts.Since.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, opts.Since.Format(time.RFC3339Nano))
	}

	query += " ORDER BY created_at DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*StoredApproval
	for rows.Next() {
		req, err := scanStoredApprovalFromRows(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}

	return requests, rows.Err()
}

// Approve approves a pending request.
func (s *ApprovalStore) Approve(ctx context.Context, approvalID, approvedBy, reason string, validFor time.Duration) error {
	now := time.Now().UTC()
	var validUntil *time.Time
	if validFor > 0 {
		t := now.Add(validFor)
		validUntil = &t
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'approved',
			resolved_by = ?,
			resolved_at = ?,
			resolution_reason = ?,
			approval_valid_until = ?,
			updated_at = ?
		WHERE approval_id = ? AND status = 'pending'
	`,
		approvedBy,
		now.Format(time.RFC3339Nano),
		reason,
		formatTimeOrNull(ptrToTime(validUntil)),
		now.Format(time.RFC3339Nano),
		approvalID,
	)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("approval %s not found or not pending", approvalID)
	}

	// Notify waiters
	s.notifyWaiters(approvalID)

	return nil
}

// Deny denies a pending request.
func (s *ApprovalStore) Deny(ctx context.Context, approvalID, deniedBy, reason string) error {
	now := time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'denied',
			resolved_by = ?,
			resolved_at = ?,
			resolution_reason = ?,
			updated_at = ?
		WHERE approval_id = ? AND status = 'pending'
	`,
		deniedBy,
		now.Format(time.RFC3339Nano),
		reason,
		now.Format(time.RFC3339Nano),
		approvalID,
	)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("approval %s not found or not pending", approvalID)
	}

	// Notify waiters
	s.notifyWaiters(approvalID)

	return nil
}

// Cancel cancels a pending request.
func (s *ApprovalStore) Cancel(ctx context.Context, approvalID, cancelledBy, reason string) error {
	now := time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'cancelled',
			resolved_by = ?,
			resolved_at = ?,
			resolution_reason = ?,
			updated_at = ?
		WHERE approval_id = ? AND status = 'pending'
	`,
		cancelledBy,
		now.Format(time.RFC3339Nano),
		reason,
		now.Format(time.RFC3339Nano),
		approvalID,
	)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("approval %s not found or not pending", approvalID)
	}

	// Notify waiters
	s.notifyWaiters(approvalID)

	return nil
}

// ExpireRequests expires all pending requests past their expiration time.
// Returns the number of expired requests.
func (s *ApprovalStore) ExpireRequests(ctx context.Context) (int, error) {
	now := time.Now().UTC()

	// Get IDs of requests to expire (for notifying waiters)
	rows, err := s.db.QueryContext(ctx, `
		SELECT approval_id FROM approval_requests
		WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at < ?
	`, now.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}

	var expiredIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		expiredIDs = append(expiredIDs, id)
	}
	rows.Close()

	if len(expiredIDs) == 0 {
		return 0, nil
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'expired',
			resolved_at = ?,
			resolution_reason = 'Approval request expired',
			updated_at = ?
		WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at < ?
	`,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}

	affected, _ := result.RowsAffected()

	// Notify waiters for expired requests
	for _, id := range expiredIDs {
		s.notifyWaiters(id)
	}

	return int(affected), nil
}

// WaitForResolution blocks until the approval is resolved or context is cancelled.
func (s *ApprovalStore) WaitForResolution(ctx context.Context, approvalID string) (*StoredApproval, error) {
	// First check if already resolved
	req, err := s.GetRequest(ctx, approvalID)
	if err != nil {
		return nil, err
	}
	if req.Status != "pending" {
		return req, nil
	}

	// Register waiter
	ch := make(chan *StoredApproval, 1)
	s.waiterMu.Lock()
	s.waiters[approvalID] = append(s.waiters[approvalID], ch)
	s.waiterMu.Unlock()

	defer func() {
		s.waiterMu.Lock()
		channels := s.waiters[approvalID]
		for i, c := range channels {
			if c == ch {
				s.waiters[approvalID] = append(channels[:i], channels[i+1:]...)
				break
			}
		}
		if len(s.waiters[approvalID]) == 0 {
			delete(s.waiters, approvalID)
		}
		s.waiterMu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case req := <-ch:
		return req, nil
	}
}

// notifyWaiters notifies all waiters for a given approval ID.
func (s *ApprovalStore) notifyWaiters(approvalID string) {
	s.waiterMu.Lock()
	channels := s.waiters[approvalID]
	delete(s.waiters, approvalID)
	s.waiterMu.Unlock()

	if len(channels) == 0 {
		return
	}

	// Fetch the updated request
	req, err := s.GetRequest(context.Background(), approvalID)
	if err != nil {
		return
	}

	for _, ch := range channels {
		select {
		case ch <- req:
		default:
		}
	}
}

// MarkCallbackSent marks the callback as sent for an approval.
func (s *ApprovalStore) MarkCallbackSent(ctx context.Context, approvalID string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET callback_sent_at = ?, updated_at = ?
		WHERE approval_id = ?
	`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), approvalID)
	return err
}

// IsApprovalValid checks if an approval is valid (approved and not expired).
func (req *StoredApproval) IsApprovalValid() bool {
	if req.Status != "approved" {
		return false
	}
	if !req.ApprovalValidUntil.IsZero() && time.Now().After(req.ApprovalValidUntil) {
		return false
	}
	return true
}

// Helper functions

func scanStoredApproval(row *sql.Row) (*StoredApproval, error) {
	var req StoredApproval
	var eventID, traceID, toolName, agentName, resourceType, resourceName sql.NullString
	var requestContext, resolvedBy, resolvedAt, resolutionReason sql.NullString
	var expiresAt, validUntil, policyName, approverRole sql.NullString
	var callbackURL, callbackSentAt sql.NullString
	var requestedAt, createdAt, updatedAt string

	err := row.Scan(
		&req.ApprovalID, &eventID, &traceID, &req.Status,
		&req.ActionClass, &toolName, &agentName, &resourceType, &resourceName,
		&req.RequestedBy, &requestedAt, &requestContext,
		&resolvedBy, &resolvedAt, &resolutionReason,
		&expiresAt, &validUntil, &policyName, &approverRole,
		&callbackURL, &callbackSentAt, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("approval not found")
		}
		return nil, err
	}

	req.EventID = eventID.String
	req.TraceID = traceID.String
	req.ToolName = toolName.String
	req.AgentName = agentName.String
	req.ResourceType = resourceType.String
	req.ResourceName = resourceName.String
	req.ResolvedBy = resolvedBy.String
	req.ResolutionReason = resolutionReason.String
	req.PolicyName = policyName.String
	req.ApproverRole = approverRole.String
	req.CallbackURL = callbackURL.String

	if requestContext.Valid {
		json.Unmarshal([]byte(requestContext.String), &req.RequestContext)
	}

	req.RequestedAt, _ = time.Parse(time.RFC3339Nano, requestedAt)
	req.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	req.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	if resolvedAt.Valid {
		req.ResolvedAt, _ = time.Parse(time.RFC3339Nano, resolvedAt.String)
	}
	if expiresAt.Valid {
		req.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt.String)
	}
	if validUntil.Valid {
		req.ApprovalValidUntil, _ = time.Parse(time.RFC3339Nano, validUntil.String)
	}
	if callbackSentAt.Valid {
		req.CallbackSentAt, _ = time.Parse(time.RFC3339Nano, callbackSentAt.String)
	}

	return &req, nil
}

func scanStoredApprovalFromRows(rows *sql.Rows) (*StoredApproval, error) {
	var req StoredApproval
	var eventID, traceID, toolName, agentName, resourceType, resourceName sql.NullString
	var requestContext, resolvedBy, resolvedAt, resolutionReason sql.NullString
	var expiresAt, validUntil, policyName, approverRole sql.NullString
	var callbackURL, callbackSentAt sql.NullString
	var requestedAt, createdAt, updatedAt string

	err := rows.Scan(
		&req.ApprovalID, &eventID, &traceID, &req.Status,
		&req.ActionClass, &toolName, &agentName, &resourceType, &resourceName,
		&req.RequestedBy, &requestedAt, &requestContext,
		&resolvedBy, &resolvedAt, &resolutionReason,
		&expiresAt, &validUntil, &policyName, &approverRole,
		&callbackURL, &callbackSentAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	req.EventID = eventID.String
	req.TraceID = traceID.String
	req.ToolName = toolName.String
	req.AgentName = agentName.String
	req.ResourceType = resourceType.String
	req.ResourceName = resourceName.String
	req.ResolvedBy = resolvedBy.String
	req.ResolutionReason = resolutionReason.String
	req.PolicyName = policyName.String
	req.ApproverRole = approverRole.String
	req.CallbackURL = callbackURL.String

	if requestContext.Valid {
		json.Unmarshal([]byte(requestContext.String), &req.RequestContext)
	}

	req.RequestedAt, _ = time.Parse(time.RFC3339Nano, requestedAt)
	req.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	req.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	if resolvedAt.Valid {
		req.ResolvedAt, _ = time.Parse(time.RFC3339Nano, resolvedAt.String)
	}
	if expiresAt.Valid {
		req.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt.String)
	}
	if validUntil.Valid {
		req.ApprovalValidUntil, _ = time.Parse(time.RFC3339Nano, validUntil.String)
	}
	if callbackSentAt.Valid {
		req.CallbackSentAt, _ = time.Parse(time.RFC3339Nano, callbackSentAt.String)
	}

	return &req, nil
}

func formatTimeOrNull(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}

func ptrToTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

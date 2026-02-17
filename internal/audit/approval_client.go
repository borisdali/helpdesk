package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ApprovalClient provides an HTTP client for agents to interact with the approval API.
type ApprovalClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewApprovalClient creates a new approval client.
// baseURL should be the auditd service URL (e.g., "http://localhost:1199").
func NewApprovalClient(baseURL string) *ApprovalClient {
	return &ApprovalClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ApprovalCreateRequest is the request body for creating an approval.
type ApprovalCreateRequest struct {
	EventID      string         `json:"event_id,omitempty"`
	TraceID      string         `json:"trace_id,omitempty"`
	ActionClass  string         `json:"action_class"`
	ToolName     string         `json:"tool_name,omitempty"`
	AgentName    string         `json:"agent_name,omitempty"`
	ResourceType string         `json:"resource_type,omitempty"`
	ResourceName string         `json:"resource_name,omitempty"`
	RequestedBy  string         `json:"requested_by"`
	Context      map[string]any `json:"request_context,omitempty"`
	PolicyName   string         `json:"policy_name,omitempty"`
	ApproverRole string         `json:"approver_role,omitempty"`
	ExpiresInMin int            `json:"expires_in_minutes,omitempty"`
	CallbackURL  string         `json:"callback_url,omitempty"`
}

// ApprovalCreateResponse is the response from creating an approval.
type ApprovalCreateResponse struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
	ExpiresAt  string `json:"expires_at"`
}

// CreateApproval creates a new approval request.
func (c *ApprovalClient) CreateApproval(ctx context.Context, req ApprovalCreateRequest) (*ApprovalCreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/approvals", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ApprovalCreateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// GetApproval retrieves an approval by ID.
func (c *ApprovalClient) GetApproval(ctx context.Context, approvalID string) (*StoredApproval, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/approvals/"+approvalID, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("approval not found: %s", approvalID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result StoredApproval
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// WaitForApproval waits for an approval to be resolved (approved/denied/expired).
// timeout specifies how long to wait before returning the current status.
func (c *ApprovalClient) WaitForApproval(ctx context.Context, approvalID string, timeout time.Duration) (*StoredApproval, error) {
	// Create a separate client with longer timeout for long-poll
	client := &http.Client{
		Timeout: timeout + 5*time.Second, // Extra buffer for HTTP overhead
	}

	u := fmt.Sprintf("%s/v1/approvals/%s/wait?timeout=%s", c.baseURL, approvalID, timeout.String())
	httpReq, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("approval not found: %s", approvalID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result StoredApproval
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// ListApprovals returns approvals matching the given filters.
type ApprovalListOptions struct {
	Status      string
	AgentName   string
	TraceID     string
	RequestedBy string
	Limit       int
}

func (c *ApprovalClient) ListApprovals(ctx context.Context, opts ApprovalListOptions) ([]StoredApproval, error) {
	u, _ := url.Parse(c.baseURL + "/v1/approvals")
	q := u.Query()
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.AgentName != "" {
		q.Set("agent", opts.AgentName)
	}
	if opts.TraceID != "" {
		q.Set("trace_id", opts.TraceID)
	}
	if opts.RequestedBy != "" {
		q.Set("requested_by", opts.RequestedBy)
	}
	if opts.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result []StoredApproval
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// CheckExistingApproval looks for an existing valid approval for the given trace+tool combination.
// Returns the approval if found and valid, nil otherwise.
func (c *ApprovalClient) CheckExistingApproval(ctx context.Context, traceID, toolName string) (*StoredApproval, error) {
	if traceID == "" {
		return nil, nil // No trace ID, can't check for existing approval
	}

	approvals, err := c.ListApprovals(ctx, ApprovalListOptions{
		TraceID: traceID,
		Status:  "approved",
		Limit:   10,
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	for _, approval := range approvals {
		// Match by tool name if specified
		if toolName != "" && approval.ToolName != toolName {
			continue
		}

		// Check if still valid
		if !approval.ApprovalValidUntil.IsZero() && approval.ApprovalValidUntil.Before(now) {
			continue // Expired
		}

		return &approval, nil
	}

	return nil, nil // No valid approval found
}

// RequestApprovalAndWait creates an approval request and waits for resolution.
// This is a convenience method that combines CreateApproval and WaitForApproval.
// Returns the resolved approval or an error if the request times out or is denied.
func (c *ApprovalClient) RequestApprovalAndWait(ctx context.Context, req ApprovalCreateRequest, waitTimeout time.Duration) (*StoredApproval, error) {
	// First check if there's an existing valid approval
	if req.TraceID != "" {
		existing, err := c.CheckExistingApproval(ctx, req.TraceID, req.ToolName)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	// Create the approval request
	createResp, err := c.CreateApproval(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create approval: %w", err)
	}

	// Wait for resolution
	approval, err := c.WaitForApproval(ctx, createResp.ApprovalID, waitTimeout)
	if err != nil {
		return nil, fmt.Errorf("wait for approval: %w", err)
	}

	return approval, nil
}

// CancelApproval cancels a pending approval request.
func (c *ApprovalClient) CancelApproval(ctx context.Context, approvalID, cancelledBy, reason string) error {
	body, _ := json.Marshal(map[string]string{
		"cancelled_by": cancelledBy,
		"reason":       reason,
	})

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/approvals/"+approvalID+"/cancel", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RemoteStore sends audit events to a central audit service via HTTP.
// It implements the same interface as Store but delegates to the service.
type RemoteStore struct {
	baseURL    string
	httpClient *http.Client
}

// NewRemoteStore creates a client for the central audit service.
func NewRemoteStore(baseURL string) *RemoteStore {
	return &RemoteStore{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// WithAPIKey returns a copy of the RemoteStore that authenticates every request
// with the given API key as a Bearer token. Used when auditd runs with
// HELPDESK_USERS_FILE set (enforcing mode) and agents have a service account.
func (r *RemoteStore) WithAPIKey(apiKey string) *RemoteStore {
	return &RemoteStore{
		baseURL: r.baseURL,
		httpClient: &http.Client{
			Timeout:   r.httpClient.Timeout,
			Transport: &bearerTransport{base: http.DefaultTransport, token: apiKey},
		},
	}
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

// Record sends an event to the audit service.
// The service handles hash chain computation.
func (r *RemoteStore) Record(ctx context.Context, event *Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/events", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send event: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("audit service returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response to get the computed hashes
	var result struct {
		EventID   string `json:"event_id"`
		EventHash string `json:"event_hash"`
		PrevHash  string `json:"prev_hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
		event.EventID = result.EventID
		event.EventHash = result.EventHash
		event.PrevHash = result.PrevHash
	}

	return nil
}

// RecordOutcome updates an event with its outcome.
func (r *RemoteStore) RecordOutcome(ctx context.Context, eventID string, outcome *Outcome) error {
	body, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("marshal outcome: %w", err)
	}

	url := fmt.Sprintf("%s/v1/events/%s/outcome", r.baseURL, eventID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send outcome: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("audit service returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Query retrieves events from the audit service. Every field forwarded here
// must have a matching reader in cmd/auditd's handleQueryEvents — EventID,
// MinConfidence, MaxConfidence, and ApprovalStatus are QueryOptions fields
// the in-process Store already supports but auditd's HTTP handler does not
// yet parse from the URL, so they are deliberately left unsent rather than
// silently ignored server-side; forwarding them needs a server-side change
// first.
func (r *RemoteStore) Query(ctx context.Context, opts QueryOptions) ([]Event, error) {
	q := url.Values{}
	if opts.SessionID != "" {
		q.Set("session_id", opts.SessionID)
	}
	if opts.TraceID != "" {
		q.Set("trace_id", opts.TraceID)
	}
	if opts.TraceIDPrefix != "" {
		q.Set("trace_id_prefix", opts.TraceIDPrefix)
	}
	if opts.EventType != "" {
		q.Set("event_type", string(opts.EventType))
	}
	if len(opts.EventTypes) > 0 {
		names := make([]string, len(opts.EventTypes))
		for i, et := range opts.EventTypes {
			names[i] = string(et)
		}
		q.Set("types", strings.Join(names, ","))
	}
	if opts.Agent != "" {
		q.Set("agent", opts.Agent)
	}
	if opts.ActionClass != "" {
		q.Set("action_class", string(opts.ActionClass))
	}
	if !opts.Since.IsZero() {
		q.Set("since", opts.Since.Format(time.RFC3339))
	}
	if opts.ToolName != "" {
		q.Set("tool_name", opts.ToolName)
	}
	if opts.OutcomeStatus != "" {
		q.Set("outcome_status", opts.OutcomeStatus)
	}
	if opts.Origin != "" {
		q.Set("origin", opts.Origin)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}

	reqURL := r.baseURL + "/v1/events"
	if encoded := q.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("audit service returned %d: %s", resp.StatusCode, string(respBody))
	}

	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}

	return events, nil
}

// Close is a no-op for RemoteStore (no local resources to clean up).
func (r *RemoteStore) Close() error {
	return nil
}

// VerifyIntegrity checks the hash chain via the audit service.
func (r *RemoteStore) VerifyIntegrity(ctx context.Context) (ChainStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/v1/verify", nil)
	if err != nil {
		return ChainStatus{}, fmt.Errorf("create request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return ChainStatus{}, fmt.Errorf("verify chain: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return ChainStatus{}, fmt.Errorf("audit service returned %d: %s", resp.StatusCode, string(respBody))
	}

	var status ChainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return ChainStatus{}, fmt.Errorf("decode result: %w", err)
	}

	return status, nil
}

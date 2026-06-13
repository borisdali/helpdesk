package faultlib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"helpdesk/testing/testutil"
)

// ctxKeyFaultTraceID is the context key for the per-fault X-Trace-ID header.
type ctxKeyFaultTraceID struct{}

// WithFaultTraceID returns ctx carrying the given trace ID.
func WithFaultTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKeyFaultTraceID{}, traceID)
}

// FaultTraceID extracts the trace ID from ctx, or returns "".
func FaultTraceID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string)
	return id
}

// Runner sends prompts to agents, routing via gateway playbook when configured.
type Runner struct {
	cfg *HarnessConfig

	// gatewayCache memoises IsGatewayURL results per URL so we only probe once.
	gatewayCache   map[string]bool
	gatewayCacheMu sync.Mutex
}

// NewRunner creates a Runner backed by cfg.
func NewRunner(cfg *HarnessConfig) *Runner {
	return &Runner{
		cfg:          cfg,
		gatewayCache: make(map[string]bool),
	}
}

// Run sends the failure prompt to the appropriate agent or gateway and returns
// the response. When ViaGateway is true and GatewayURL is set, calls are
// routed through the gateway: via the playbook endpoint when
// DiagnosisPlaybookSeriesID is set, otherwise via POST /api/v1/query.
// When ViaGateway is false, the category-specific agent URL is used; if that
// URL is itself a helpdesk gateway it is auto-detected and routed via the REST
// API rather than A2A.
func (r *Runner) Run(ctx context.Context, f Failure) testutil.AgentResponse {
	if r.cfg.ViaGateway && r.cfg.GatewayURL != "" {
		if f.DiagnosisPlaybookSeriesID != "" {
			return r.runViaPlaybook(ctx, f)
		}
		return r.runViaGatewayQuery(ctx, f)
	}

	prompt := ResolvePrompt(f.Prompt, r.cfg)
	agentURL := r.agentURL(f.Category)
	if agentURL == "" {
		return testutil.AgentResponse{Error: fmt.Errorf("no agent URL configured for category %q", f.Category)}
	}

	slog.Info("sending prompt to agent",
		"failure", f.ID,
		"category", f.Category,
		"agent", agentURL,
		"prompt_len", len(prompt),
	)

	timeout := f.TimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if r.isGateway(ctx, agentURL) {
		agentName := categoryToGatewayAgent(f.Category)
		if r.cfg.GatewayAPIKey == "" {
			slog.Warn("gateway detected but no API key set — requests may return 401")
		}
		slog.Info("using gateway REST API", "agent_name", agentName, "purpose", r.cfg.GatewayPurpose)
		return testutil.SendPromptViaGateway(ctx, agentURL, r.cfg.GatewayAPIKey, agentName, prompt, r.cfg.GatewayPurpose, r.cfg.OperatorID)
	}
	return testutil.SendPrompt(ctx, agentURL, prompt)
}

// runViaGatewayQuery routes the fault prompt through the gateway's
// /api/v1/query endpoint using the fault category as the agent name. Used when
// ViaGateway=true but the fault has no DiagnosisPlaybookSeriesID.
func (r *Runner) runViaGatewayQuery(ctx context.Context, f Failure) testutil.AgentResponse {
	agentName := categoryToGatewayAgent(f.Category)
	prompt := ResolvePrompt(f.Prompt, r.cfg)

	slog.Info("sending prompt via gateway query",
		"failure", f.ID, "category", f.Category, "agent", agentName, "gateway", r.cfg.GatewayURL)

	timeout := f.TimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return testutil.SendPromptViaGateway(ctx, r.cfg.GatewayURL, r.cfg.GatewayAPIKey, agentName, prompt, r.cfg.GatewayPurpose, r.cfg.OperatorID)
}

// runViaPlaybook routes diagnosis through the gateway's playbook endpoint.
func (r *Runner) runViaPlaybook(ctx context.Context, f Failure) testutil.AgentResponse {
	start := time.Now()
	client := &http.Client{Timeout: f.TimeoutDuration() + 10*time.Second}

	playbookID, err := r.resolvePlaybookID(ctx, client, f.DiagnosisPlaybookSeriesID)
	if err != nil {
		return testutil.AgentResponse{
			Duration: time.Since(start),
			Error:    fmt.Errorf("resolving diagnosis playbook %q: %w", f.DiagnosisPlaybookSeriesID, err),
		}
	}

	slog.Info("sending prompt to agent via playbook",
		"failure", f.ID,
		"series_id", f.DiagnosisPlaybookSeriesID,
		"playbook_id", playbookID,
		"gateway", r.cfg.GatewayURL,
	)

	connStr := r.cfg.ConnStr
	if r.cfg.AgentConnStr != "" {
		connStr = r.cfg.AgentConnStr
	}
	reqBody := map[string]any{
		"context": ResolvePrompt(f.Prompt, r.cfg),
	}
	if connStr != "" {
		reqBody["connection_string"] = connStr
	}
	if r.cfg.ApprovalMode != "" {
		reqBody["approval_mode"] = r.cfg.ApprovalMode
	}
	if r.cfg.GateEscalation {
		reqBody["gate_escalation"] = true
		if f.Remediation.PlaybookID != "" {
			reqBody["remediation_series_id"] = f.Remediation.PlaybookID
		}
	}
	body, _ := json.Marshal(reqBody)

	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbooks/" + playbookID + "/run"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return testutil.AgentResponse{Duration: time.Since(start), Error: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", r.cfg.GatewayPurpose)
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	if r.cfg.OperatorID != "" {
		req.Header.Set("X-User", r.cfg.OperatorID)
	}
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
		req.Header.Set("X-Trace-ID", id)
	}

	resp, err := client.Do(req)
	if err != nil {
		return testutil.AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("POST %s: %w", reqURL, err)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	duration := time.Since(start)

	if resp.StatusCode >= 300 {
		return testutil.AgentResponse{Duration: duration, Error: fmt.Errorf("playbook run returned %d: %s", resp.StatusCode, string(respBody))}
	}

	var result struct {
		Text               string         `json:"text"`
		CrystalBall        bool           `json:"crystal_ball"`
		Error              string         `json:"error"`
		ToolCalls          []string       `json:"tool_calls"`
		Warnings           []string       `json:"warnings"`
		RunID              string         `json:"run_id"`
		Status             string         `json:"status"`
		TransitionTarget   string         `json:"transition_target,omitempty"`
		EscalationTarget   string         `json:"escalation_target,omitempty"`
		EscalationFindings string         `json:"escalation_findings"`
		ConfidenceWarning  string         `json:"confidence_warning"`
		SuggestedMode      string         `json:"suggested_approval_mode"`
		RemediationPreview map[string]any `json:"remediation_preview,omitempty"`
		DiagnosticReport   map[string]any `json:"diagnostic_report,omitempty"`
		GateReason         string         `json:"gate_reason,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return testutil.AgentResponse{Duration: duration, Error: fmt.Errorf("decoding playbook response: %w", err)}
	}
	if result.Error != "" {
		return testutil.AgentResponse{Duration: duration, Error: fmt.Errorf("playbook error: %s", result.Error)}
	}
	for _, w := range result.Warnings {
		slog.Warn("gateway warning", "failure", f.ID, "warning", w)
	}
	ar := testutil.AgentResponse{
		Text:               result.Text,
		CrystalBall:        result.CrystalBall,
		Duration:           duration,
		Warnings:           result.Warnings,
		RunID:              result.RunID,
		Status:             result.Status,
		TransitionTarget:   result.TransitionTarget,
		EscalationTarget:   result.EscalationTarget,
		EscalationFindings: result.EscalationFindings,
		ConfidenceWarning:  result.ConfidenceWarning,
		SuggestedMode:      result.SuggestedMode,
		RemediationPreview: result.RemediationPreview,
		DiagnosticReport:   result.DiagnosticReport,
		GateReason:         result.GateReason,
	}
	if len(result.ToolCalls) > 0 {
		lower := strings.ToLower(result.Text)
		ar.ToolCalls = make([]testutil.ToolCallResult, len(result.ToolCalls))
		for i, name := range result.ToolCalls {
			sentinel := strings.ToLower("ERROR — " + name + " failed")
			ar.ToolCalls[i] = testutil.ToolCallResult{
				Name:    name,
				Success: !strings.Contains(lower, sentinel),
			}
		}
	}
	return ar
}

// resolvePlaybookID resolves a series_id to the active versioned playbook_id.
func (r *Runner) resolvePlaybookID(ctx context.Context, client *http.Client, seriesID string) (string, error) {
	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbooks?series_id=" + seriesID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	if r.cfg.OperatorID != "" {
		req.Header.Set("X-User", r.cfg.OperatorID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned %d for series %q", resp.StatusCode, seriesID)
	}
	var result struct {
		Playbooks []struct {
			PlaybookID string `json:"playbook_id"`
		} `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Playbooks) == 0 {
		return "", fmt.Errorf("no active playbook found for series %q", seriesID)
	}
	return result.Playbooks[0].PlaybookID, nil
}

// isGateway returns true if url is a helpdesk gateway, caching the result.
func (r *Runner) isGateway(ctx context.Context, url string) bool {
	r.gatewayCacheMu.Lock()
	defer r.gatewayCacheMu.Unlock()
	if cached, ok := r.gatewayCache[url]; ok {
		return cached
	}
	result := testutil.IsGatewayURL(ctx, url)
	r.gatewayCache[url] = result
	return result
}

// agentURL returns the configured agent URL for the given fault category.
func (r *Runner) agentURL(category string) string {
	switch category {
	case "database":
		return r.cfg.DBAgentURL
	case "kubernetes":
		return r.cfg.K8sAgentURL
	case "host":
		return r.cfg.SysadminAgentURL
	case "compound":
		if r.cfg.OrchestratorURL != "" {
			return r.cfg.OrchestratorURL
		}
		return r.cfg.DBAgentURL
	default:
		return ""
	}
}

// categoryToGatewayAgent maps a fault category to the gateway's agent name.
// "k8s" and "sysadmin" match the gateway's agentAliases map.
func categoryToGatewayAgent(category string) string {
	switch category {
	case "database":
		return "database"
	case "kubernetes":
		return "k8s"
	case "host":
		return "sysadmin"
	case "compound":
		return "database"
	default:
		return "database"
	}
}

package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
)

// AgentResponse captures the result of sending a prompt to an agent.
type AgentResponse struct {
	Text      string
	ToolCalls []ToolCallResult // nil when structured data is unavailable (gateway path)
	Duration  time.Duration
	Error     error
}

// ToolCallResult records one tool invocation observed in a structured A2A response.
// Success is determined by whether an "ERROR — {Name}" sentinel appears in the
// surrounding response text (set by the caller after text extraction).
type ToolCallResult struct {
	Name    string
	Success bool
}

// SendPrompt sends a text prompt to an A2A agent and returns the response.
func SendPrompt(ctx context.Context, agentURL, prompt string) AgentResponse {
	start := time.Now()

	// Fetch agent card.
	cardURL := strings.TrimSuffix(agentURL, "/") + "/.well-known/agent-card.json"
	card, err := fetchCard(ctx, cardURL)
	if err != nil {
		return AgentResponse{
			Duration: time.Since(start),
			Error:    fmt.Errorf("fetching agent card from %s: %v", cardURL, err),
		}
	}

	// Override the host in card.URL with the host from agentURL. Agent cards
	// advertise their Docker-internal hostname (e.g. http://research-agent:1106/invoke)
	// which is not reachable from outside the Docker network. Replace it with the
	// externally-reachable host that the caller used to fetch the card.
	if card.URL != "" {
		if overridden, oErr := overrideCardHost(card.URL, agentURL); oErr == nil {
			card.URL = overridden
		}
	}

	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		return AgentResponse{
			Duration: time.Since(start),
			Error:    fmt.Errorf("creating A2A client for %s: %v", agentURL, err),
		}
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: prompt})
	result, err := client.SendMessage(ctx, &a2a.MessageSendParams{Message: msg})
	if err != nil {
		return AgentResponse{
			Duration: time.Since(start),
			Error:    fmt.Errorf("A2A call to %s: %v", agentURL, err),
		}
	}

	text, toolCalls := extractResponse(result)
	return AgentResponse{
		Text:      text,
		ToolCalls: toolCalls,
		Duration:  time.Since(start),
	}
}

// overrideCardHost replaces the host (and scheme) in cardURL with the host
// from agentURL. This corrects Docker-internal hostnames advertised in agent
// cards so they resolve correctly from outside the Docker network.
func overrideCardHost(cardURL, agentURL string) (string, error) {
	cu, err := url.Parse(cardURL)
	if err != nil {
		return cardURL, err
	}
	au, err := url.Parse(agentURL)
	if err != nil {
		return cardURL, err
	}
	cu.Host = au.Host
	cu.Scheme = au.Scheme
	return cu.String(), nil
}

func fetchCard(ctx context.Context, cardURL string) (*a2a.AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, cardURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, err
	}
	return &card, nil
}

// extractResponse pulls the response text and structured tool call list from a
// SendMessageResult. ADK-based agents (via agentutil) emit a DataPart with
// metadata helpdesk_type="tool_call_summary" carrying the list of tool names
// invoked during the request. When present, ToolCalls is populated with exact
// names; Success is derived by checking whether an error sentinel for that name
// appears in the response text.
//
// When the DataPart is absent (non-ADK agents, gateway path), toolCalls is nil
// and the evaluator falls back to section-aware text matching with a warning.
func extractResponse(result a2a.SendMessageResult) (text string, toolCalls []ToolCallResult) {
	switch v := result.(type) {
	case *a2a.Task:
		var summaryNames []string

		// ADK agents emit artifact update events; text and the tool-call DataPart
		// live in Artifacts. Scan all artifacts: collect text from TextParts and
		// tool call names from the summary DataPart.
		for _, artifact := range v.Artifacts {
			for _, part := range artifact.Parts {
				switch p := part.(type) {
				case a2a.TextPart:
					if text == "" {
						text = p.Text
					}
				case a2a.DataPart:
					meta, _ := p.Metadata["helpdesk_type"].(string)
					if meta == "tool_call_summary" {
						if names, ok := p.Data["tool_calls"].([]any); ok {
							for _, n := range names {
								if s, ok := n.(string); ok {
									summaryNames = append(summaryNames, s)
								}
							}
						}
					}
				}
			}
		}

		// Non-ADK agents or error responses may use Status.Message.
		if text == "" && v.Status.Message != nil {
			text = partsText(v.Status.Message.Parts)
		}
		// Some implementations populate History.
		if text == "" {
			for i := len(v.History) - 1; i >= 0; i-- {
				if v.History[i].Role == a2a.MessageRoleAgent {
					if t := partsText(v.History[i].Parts); t != "" {
						text = t
						break
					}
				}
			}
		}

		if len(summaryNames) > 0 {
			lower := strings.ToLower(text)
			toolCalls = make([]ToolCallResult, len(summaryNames))
			for i, name := range summaryNames {
				// A tool failed when the agent emits "ERROR — {name} failed".
				sentinel := strings.ToLower("ERROR — " + name + " failed")
				toolCalls[i] = ToolCallResult{
					Name:    name,
					Success: !strings.Contains(lower, sentinel),
				}
			}
		}

	case *a2a.Message:
		text = partsText(v.Parts)
	}
	return text, toolCalls
}

// IsGatewayURL probes whether url is a helpdesk gateway by checking whether
// GET {url}/api/v1/agents returns a recognisable HTTP status (200 or 401).
// A 404 (or connection error) means it is an A2A agent endpoint instead.
func IsGatewayURL(ctx context.Context, baseURL string) bool {
	probeURL := strings.TrimSuffix(baseURL, "/") + "/api/v1/agents"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
}

// SendPromptViaGateway sends a prompt to a helpdesk gateway via
// POST /api/v1/query and returns the agent's response text.
// agentName should be "database", "kubernetes", "sysadmin", etc.
// apiKey is the Bearer token for gateway auth (may be empty for unauthenticated gateways).
// purpose is the declared access purpose (e.g. "diagnostic"); an empty string omits the field.
func SendPromptViaGateway(ctx context.Context, gatewayURL, apiKey, agentName, prompt, purpose string) AgentResponse {
	start := time.Now()

	reqBody := map[string]string{
		"agent":   agentName,
		"message": prompt,
	}
	if purpose != "" {
		reqBody["purpose"] = purpose
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("marshalling query: %v", err)}
	}

	queryURL := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, queryURL, bytes.NewReader(body))
	if err != nil {
		return AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("building gateway request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("POST %s: %v", queryURL, err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("reading gateway response: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return AgentResponse{
			Duration: time.Since(start),
			Error:    fmt.Errorf("gateway returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))),
		}
	}

	var result struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("decoding gateway response: %v", err)}
	}
	if result.Error != "" {
		return AgentResponse{Duration: time.Since(start), Error: fmt.Errorf("gateway error: %s", result.Error)}
	}
	return AgentResponse{Text: result.Text, Duration: time.Since(start)}
}

func partsText(parts a2a.ContentParts) string {
	var texts []string
	for _, p := range parts {
		if tp, ok := p.(a2a.TextPart); ok {
			texts = append(texts, tp.Text)
		}
	}
	return strings.Join(texts, "\n")
}

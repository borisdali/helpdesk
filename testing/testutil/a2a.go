package testutil

import (
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
	Text     string
	Duration time.Duration
	Error    error
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

	text := extractText(result)
	return AgentResponse{
		Text:     text,
		Duration: time.Since(start),
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

// extractText pulls the response text from a SendMessageResult.
// ADK-based agents store their response in Artifacts; other agents may use
// History or Status.Message. We check all three locations.
func extractText(result a2a.SendMessageResult) string {
	switch v := result.(type) {
	case *a2a.Task:
		// ADK agents emit artifact update events; text lives in Artifacts.
		for _, artifact := range v.Artifacts {
			if t := partsText(artifact.Parts); t != "" {
				return t
			}
		}
		// Non-ADK agents or error responses may use Status.Message.
		if v.Status.Message != nil {
			if t := partsText(v.Status.Message.Parts); t != "" {
				return t
			}
		}
		// Some implementations populate History.
		for i := len(v.History) - 1; i >= 0; i-- {
			if v.History[i].Role == a2a.MessageRoleAgent {
				if t := partsText(v.History[i].Parts); t != "" {
					return t
				}
			}
		}
	case *a2a.Message:
		return partsText(v.Parts)
	}
	return ""
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

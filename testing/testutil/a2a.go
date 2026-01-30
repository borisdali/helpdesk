package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
func extractText(result a2a.SendMessageResult) string {
	switch v := result.(type) {
	case *a2a.Task:
		if v.Status.Message != nil {
			if t := partsText(v.Status.Message.Parts); t != "" {
				return t
			}
		}
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

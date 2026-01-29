// Package discovery provides agent card fetching and parsing for A2A agents.
package discovery

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
)

// Agent holds the discovered agent metadata.
type Agent struct {
	Name      string
	InvokeURL string
	Card      *a2a.AgentCard
}

// Discover fetches agent cards from a list of base URLs and returns
// a map keyed by agent name. Agents that cannot be reached are logged
// and skipped.
func Discover(baseURLs []string) (map[string]*Agent, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	agents := make(map[string]*Agent)

	for _, baseURL := range baseURLs {
		cardURL := strings.TrimSuffix(baseURL, "/") + "/.well-known/agent-card.json"
		slog.Info("discovering agent", "url", cardURL)

		resp, err := client.Get(cardURL)
		if err != nil {
			slog.Warn("discovery: failed to fetch agent card", "url", cardURL, "err", err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			slog.Warn("discovery: non-200 status", "url", cardURL, "status", resp.StatusCode)
			continue
		}

		var card a2a.AgentCard
		if err := json.Unmarshal(body, &card); err != nil {
			slog.Warn("discovery: failed to parse agent card", "url", cardURL, "err", err)
			continue
		}

		agents[card.Name] = &Agent{
			Name:      card.Name,
			InvokeURL: card.URL,
			Card:      &card,
		}
		slog.Info("discovered agent", "name", card.Name, "invoke_url", card.URL)
	}

	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents discovered from %d URLs", len(baseURLs))
	}
	return agents, nil
}

// Package discovery provides agent card fetching and parsing for A2A agents.
package discovery

import (
	"context"
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
	// Schemas maps tool name → JSON Schema properties, fetched from GET /schemas.
	// Nil if the agent did not expose the endpoint.
	Schemas map[string]map[string]any
}

// Discover fetches agent cards from a list of base URLs and returns
// a map keyed by agent name. Agents that cannot be reached are logged
// and skipped.
func Discover(baseURLs []string) (map[string]*Agent, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	agents := make(map[string]*Agent)

	for _, baseURL := range baseURLs {
		base := strings.TrimSuffix(baseURL, "/")
		cardURL := base + "/.well-known/agent-card.json"
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

		// Use the discovery base URL rather than the agent's self-reported
		// card.URL, which is typically a container-local address like
		// http://[::]:1100 that isn't reachable from other containers.
		invokeURL := base + "/invoke"
		card.URL = invokeURL

		// Fetch tool schemas from the agent's /schemas endpoint.
		// This endpoint is required for schema fingerprinting and planner accuracy.
		var schemas map[string]map[string]any
		schemasURL := base + "/schemas"
		sresp, err := client.Get(schemasURL)
		if err != nil {
			slog.Error("discovery: failed to fetch tool schemas — skipping agent", "agent", card.Name, "url", schemasURL, "err", err)
			continue
		}
		sbody, _ := io.ReadAll(sresp.Body)
		sresp.Body.Close()
		if sresp.StatusCode != http.StatusOK {
			slog.Error("discovery: non-200 status fetching tool schemas — skipping agent", "agent", card.Name, "url", schemasURL, "status", sresp.StatusCode)
			continue
		}
		if err := json.Unmarshal(sbody, &schemas); err != nil {
			slog.Error("discovery: failed to parse tool schemas — skipping agent", "agent", card.Name, "url", schemasURL, "err", err)
			continue
		}

		agents[card.Name] = &Agent{
			Name:      card.Name,
			InvokeURL: invokeURL,
			Card:      &card,
			Schemas:   schemas,
		}
		slog.Info("discovered agent", "name", card.Name, "invoke_url", invokeURL, "schemas", len(schemas))
	}

	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents discovered from %d URLs", len(baseURLs))
	}
	return agents, nil
}

// DiscoverWithRetry calls Discover repeatedly until all configured URLs have
// responded or the context deadline is exceeded. On timeout it returns whatever
// agents were found so far (provided at least one responded); if none ever
// responded it returns an error.
func DiscoverWithRetry(ctx context.Context, baseURLs []string, retryInterval time.Duration) (map[string]*Agent, error) {
	// Filter out syntactically invalid URLs up front so they don't count
	// toward the expected total.
	var validURLs []string
	for _, u := range baseURLs {
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			validURLs = append(validURLs, u)
		} else {
			slog.Warn("discovery: skipping invalid URL", "url", u)
		}
	}
	if len(validURLs) == 0 {
		return nil, fmt.Errorf("no valid agent URLs provided")
	}

	accumulated := make(map[string]*Agent)
	pending := make([]string, len(validURLs))
	copy(pending, validURLs)
	attempt := 0

	for len(pending) > 0 {
		attempt++
		found, _ := Discover(pending)
		for name, a := range found {
			accumulated[name] = a
		}

		// Remove URLs whose agents are now known.
		var stillPending []string
		for _, u := range pending {
			discovered := false
			for _, a := range accumulated {
				if strings.HasPrefix(a.InvokeURL, strings.TrimSuffix(u, "/")+"/") {
					discovered = true
					break
				}
			}
			if !discovered {
				stillPending = append(stillPending, u)
			}
		}
		pending = stillPending

		if len(pending) == 0 {
			if attempt > 1 {
				slog.Info("all agents discovered", "attempt", attempt, "count", len(accumulated))
			}
			return accumulated, nil
		}

		slog.Warn("waiting for agents", "attempt", attempt, "pending", len(pending), "ready", len(accumulated), "retry_in", retryInterval)

		select {
		case <-ctx.Done():
			if len(accumulated) > 0 {
				slog.Warn("discovery timed out with partial results", "ready", len(accumulated), "missing", len(pending))
				return accumulated, nil
			}
			return nil, fmt.Errorf("agent discovery timed out after %d attempt(s): no agents responded", attempt)
		case <-time.After(retryInterval):
		}
	}
	return accumulated, nil
}

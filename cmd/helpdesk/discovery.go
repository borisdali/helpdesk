package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// agentCardResponse represents the relevant fields from /.well-known/agent-card.json
type agentCardResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Skills      []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"skills,omitempty"`
}

// discoverAgentFromURL fetches the agent card from a URL and converts it to AgentConfig.
func discoverAgentFromURL(baseURL string) (*AgentConfig, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	cardURL := strings.TrimSuffix(baseURL, "/") + "/.well-known/agent-card.json"
	resp, err := client.Get(cardURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent card: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent card returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent card: %v", err)
	}

	var card agentCardResponse
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("failed to parse agent card: %v", err)
	}

	config := &AgentConfig{
		Name:        card.Name,
		Description: card.Description,
		URL:         baseURL,
	}

	for _, skill := range card.Skills {
		if skill.Description != "" {
			config.UseCases = append(config.UseCases, skill.Description)
		} else if skill.Name != "" {
			config.UseCases = append(config.UseCases, skill.Name)
		}
	}

	if config.Name == "" {
		return nil, fmt.Errorf("agent card missing name")
	}

	return config, nil
}

// discoverAgents discovers agents from a list of base URLs by fetching their agent cards.
func discoverAgents(urls []string) ([]AgentConfig, []string) {
	var discovered []AgentConfig
	var failed []string

	for _, url := range urls {
		slog.Info("discovering agent", "url", url)
		config, err := discoverAgentFromURL(url)
		if err != nil {
			slog.Warn("agent discovery failed", "url", url, "err", err)
			failed = append(failed, url)
			continue
		}
		slog.Info("discovered agent", "name", config.Name, "url", url)
		discovered = append(discovered, *config)
	}

	return discovered, failed
}

// checkAgentHealth verifies that an agent is reachable by fetching its agent card.
func checkAgentHealth(agentURL string) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	cardURL := strings.TrimSuffix(agentURL, "/") + "/.well-known/agent-card.json"
	resp, err := client.Get(cardURL)
	if err != nil {
		return fmt.Errorf("failed to reach agent at %s: %v", agentURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent at %s returned status %d", agentURL, resp.StatusCode)
	}

	return nil
}

package audit

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
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// DelegateArgs contains the structured reasoning for a delegation decision.
type DelegateArgs struct {
	// Agent is the name of the agent to delegate to.
	Agent string `json:"agent" jsonschema:"Name of the agent to delegate to (e.g. postgres_database_agent, k8s_agent, incident_agent, or research_agent)"`

	// RequestCategory classifies the type of request.
	RequestCategory string `json:"request_category" jsonschema:"Category of the request: database, kubernetes, incident, or research"`

	// Confidence is the confidence level (0.0 to 1.0) in this routing decision.
	Confidence float64 `json:"confidence" jsonschema:"Confidence level in this routing decision from 0.0 to 1.0"`

	// UserIntent describes what the user is trying to accomplish.
	UserIntent string `json:"user_intent" jsonschema:"Brief description of what the user is trying to accomplish"`

	// ReasoningChain is the step-by-step reasoning for this decision.
	ReasoningChain []string `json:"reasoning_chain" jsonschema:"Step-by-step reasoning explaining why this agent was chosen"`

	// AlternativesConsidered lists other agents that were considered.
	AlternativesConsidered []AlternativeArg `json:"alternatives_considered" jsonschema:"Other agents that were considered but not chosen"`

	// Message is the actual message to send to the agent.
	Message string `json:"message" jsonschema:"The message to send to the delegated agent including all necessary parameters like connection_string"`
}

// AlternativeArg represents an agent that was considered but not chosen.
type AlternativeArg struct {
	Agent           string `json:"agent" jsonschema:"Name of the alternative agent"`
	RejectedBecause string `json:"rejected_because" jsonschema:"Reason this agent was not chosen"`
}

// DelegateResult contains the response from the delegated agent.
type DelegateResult struct {
	Agent    string `json:"agent"`
	Response string `json:"response"`
	Duration string `json:"duration"`
	EventID  string `json:"event_id"`
}

// AgentRegistry maps agent names to their URLs for delegation.
type AgentRegistry struct {
	agents map[string]string // name -> URL
}

// NewAgentRegistry creates a new agent registry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		agents: make(map[string]string),
	}
}

// Register adds an agent to the registry.
func (r *AgentRegistry) Register(name, url string) {
	r.agents[name] = url
}

// Get returns the URL for an agent, or empty string if not found.
func (r *AgentRegistry) Get(name string) string {
	return r.agents[name]
}

// List returns all registered agent names.
func (r *AgentRegistry) List() []string {
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	return names
}

// DelegateTool creates the delegate_to_agent tool with audit logging.
func DelegateTool(store *Store, registry *AgentRegistry, sessionID, userID string) (tool.Tool, error) {
	delegationCount := 0

	delegateFunc := func(ctx tool.Context, args DelegateArgs) (DelegateResult, error) {
		start := time.Now()
		delegationCount++
		slog.Debug("delegate_to_agent tool called",
			"agent", args.Agent,
			"category", args.RequestCategory,
			"confidence", args.Confidence,
			"reasoning", args.ReasoningChain)

		// Create audit event
		event := &Event{
			EventID:   "evt_" + uuid.New().String()[:8],
			Timestamp: start,
			EventType: EventTypeDelegation,
			Session: Session{
				ID:              sessionID,
				UserID:          userID,
				StartedAt:       start, // Will be overwritten if we track session start
				DelegationCount: delegationCount,
			},
			Input: Input{
				UserQuery: args.Message,
			},
			Decision: &Decision{
				Agent:           args.Agent,
				RequestCategory: RequestCategory(args.RequestCategory),
				Confidence:      args.Confidence,
				UserIntent:      args.UserIntent,
				ReasoningChain:  args.ReasoningChain,
				AlternativesConsidered: func() []Alternative {
					alts := make([]Alternative, len(args.AlternativesConsidered))
					for i, a := range args.AlternativesConsidered {
						alts[i] = Alternative{
							Agent:           a.Agent,
							RejectedBecause: a.RejectedBecause,
						}
					}
					return alts
				}(),
			},
		}

		// Record the delegation decision
		if store != nil {
			if err := store.Record(context.Background(), event); err != nil {
				// Log but don't fail the delegation
				slog.Warn("failed to record audit event", "error", err)
			}
		}

		// Look up the agent URL
		agentURL := registry.Get(args.Agent)
		if agentURL == "" {
			outcome := &Outcome{
				Status:       "error",
				ErrorMessage: fmt.Sprintf("agent %q not found in registry", args.Agent),
				Duration:     time.Since(start),
			}
			if store != nil {
				store.RecordOutcome(context.Background(), event.EventID, outcome)
			}
			return DelegateResult{
				Agent:    args.Agent,
				Response: fmt.Sprintf("Error: agent %q is not available. Available agents: %v", args.Agent, registry.List()),
				Duration: time.Since(start).String(),
				EventID:  event.EventID,
			}, nil
		}

		// Call the agent via A2A
		slog.Debug("calling agent via A2A",
			"agent", args.Agent,
			"url", agentURL,
			"message", args.Message)
		response, err := callAgent(context.Background(), agentURL, args.Message)
		duration := time.Since(start)
		slog.Debug("agent response received",
			"agent", args.Agent,
			"response_len", len(response),
			"duration", duration,
			"error", err)
		if len(response) > 200 {
			slog.Debug("response preview", "preview", response[:200]+"...")
		} else if len(response) > 0 {
			slog.Debug("response preview", "preview", response)
		}

		// Record outcome
		outcome := &Outcome{
			Duration: duration,
		}
		if err != nil {
			outcome.Status = "error"
			outcome.ErrorMessage = err.Error()
		} else {
			outcome.Status = "success"
		}
		if store != nil {
			store.RecordOutcome(context.Background(), event.EventID, outcome)
		}

		if err != nil {
			return DelegateResult{
				Agent:    args.Agent,
				Response: fmt.Sprintf("Error calling agent: %v", err),
				Duration: duration.String(),
				EventID:  event.EventID,
			}, nil
		}

		return DelegateResult{
			Agent:    args.Agent,
			Response: response,
			Duration: duration.String(),
			EventID:  event.EventID,
		}, nil
	}

	return functiontool.New(functiontool.Config{
		Name: "delegate_to_agent",
		Description: `Delegate a task to a specialist agent. You MUST use this tool for ALL delegations.
Before calling, provide your reasoning chain explaining why this agent was chosen.
Available agents: postgres_database_agent (database issues), k8s_agent (Kubernetes issues),
incident_agent (incident bundles), research_agent (web search for current info).`,
	}, delegateFunc)
}

// callAgent sends a message to an A2A agent and returns the response text.
func callAgent(ctx context.Context, agentURL, message string) (string, error) {
	// Fetch agent card
	cardURL := strings.TrimSuffix(agentURL, "/") + "/.well-known/agent-card.json"
	card, err := fetchAgentCard(ctx, cardURL)
	if err != nil {
		return "", fmt.Errorf("fetching agent card: %w", err)
	}

	// Create A2A client
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		return "", fmt.Errorf("creating A2A client: %w", err)
	}

	// Send message
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: message})
	result, err := client.SendMessage(ctx, &a2a.MessageSendParams{Message: msg})
	if err != nil {
		return "", fmt.Errorf("sending message: %w", err)
	}

	// Extract text from response
	return extractResponseText(result), nil
}

func fetchAgentCard(ctx context.Context, cardURL string) (*a2a.AgentCard, error) {
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

func extractResponseText(result a2a.SendMessageResult) string {
	switch v := result.(type) {
	case *a2a.Task:
		// Try status message first
		if v.Status.Message != nil {
			if t := partsToText(v.Status.Message.Parts); t != "" {
				return t
			}
		}

		// Try history (last agent message)
		for i := len(v.History) - 1; i >= 0; i-- {
			if v.History[i].Role == a2a.MessageRoleAgent {
				if t := partsToText(v.History[i].Parts); t != "" {
					return t
				}
			}
		}

		// Try artifacts (ADK puts agent responses in artifacts)
		for _, artifact := range v.Artifacts {
			if t := partsToText(artifact.Parts); t != "" {
				return t
			}
		}

	case *a2a.Message:
		return partsToText(v.Parts)
	}
	return ""
}

func partsToText(parts a2a.ContentParts) string {
	var texts []string
	for _, p := range parts {
		if tp, ok := p.(a2a.TextPart); ok {
			texts = append(texts, tp.Text)
		}
	}
	return strings.Join(texts, "\n")
}

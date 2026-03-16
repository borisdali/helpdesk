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
// auditURL is the base URL of the auditd service used for post-delegation
// verification queries; pass "" to disable verification.
// callerName is the name of the orchestrator agent (e.g. "helpdesk_orchestrator");
// it is recorded in audit events and surfaced as the journey agent name.
// It also creates and returns a DelegationGuard shared with NoDelegationCallback.
func DelegateTool(auditor Auditor, auditURL string, registry *AgentRegistry, sessionID, userID, callerName string) (tool.Tool, *DelegationGuard, error) {
	return DelegateToolWithTrace(auditor, auditURL, registry, sessionID, userID, "", callerName)
}

// DelegateToolWithTrace creates the delegate_to_agent tool with audit logging and trace ID.
// The returned DelegationGuard must be passed to NoDelegationCallback so the callback
// can detect invocations where delegate_to_agent was not called.
func DelegateToolWithTrace(auditor Auditor, auditURL string, registry *AgentRegistry, sessionID, userID, traceID, callerName string) (tool.Tool, *DelegationGuard, error) {
	guard := NewDelegationGuard()
	delegationCount := 0

	// Generate trace ID if not provided (top-level orchestrator request)
	if traceID == "" {
		traceID = NewTraceID()
	}

	delegateFunc := func(ctx tool.Context, args DelegateArgs) (DelegateResult, error) {
		// Mark this invocation as having called delegate_to_agent so
		// NoDelegationCallback skips correction injection.
		guard.MarkCalled(ctx.InvocationID())
		start := time.Now()
		delegationCount++

		// Classify the action based on the message content
		actionClass := ClassifyDelegation(args.Agent, args.Message)

		slog.Debug("delegate_to_agent tool called",
			"agent", args.Agent,
			"category", args.RequestCategory,
			"confidence", args.Confidence,
			"action_class", actionClass,
			"reasoning", args.ReasoningChain)

		// Create audit event
		event := &Event{
			EventID:     "evt_" + uuid.New().String()[:8],
			Timestamp:   start.UTC(),
			EventType:   EventTypeDelegation,
			TraceID:     traceID,
			ActionClass: actionClass,
			Session: Session{
				ID:              sessionID,
				UserID:          userID,
				AgentName:       callerName,
				StartedAt:       start, // Will be overwritten if we track session start
				DelegationCount: delegationCount,
			},
			Input: Input{
				UserQuery: args.UserIntent,
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
		if auditor != nil {
			if err := auditor.Record(context.Background(), event); err != nil {
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
			if auditor != nil {
				auditor.RecordOutcome(context.Background(), event.EventID, outcome)
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
			"message", args.Message,
			"trace_id", traceID)
		response, err := callAgentWithTrace(context.Background(), agentURL, args.Message, traceID)
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
		if auditor != nil {
			auditor.RecordOutcome(context.Background(), event.EventID, outcome)
		}

		if err != nil {
			return DelegateResult{
				Agent:    args.Agent,
				Response: fmt.Sprintf("Error calling agent: %v", err),
				Duration: duration.String(),
				EventID:  event.EventID,
			}, nil
		}

		// Post-delegation audit verification: query the audit trail to confirm
		// which tools the sub-agent actually executed, independent of its text
		// response. This closes the gap where an LLM can fabricate a success
		// message without calling any tool.
		verif := buildDelegationVerification(auditURL, traceID, start, actionClass, event.EventID, args.Agent)
		if auditor != nil {
			verifEvent := &Event{
				EventID:                "evt_" + uuid.New().String()[:8],
				Timestamp:              time.Now().UTC(),
				EventType:              EventTypeDelegationVerification,
				TraceID:                traceID,
				Session:                event.Session,
				DelegationVerification: verif,
			}
			if verifErr := auditor.Record(context.Background(), verifEvent); verifErr != nil {
				slog.Warn("failed to record delegation verification event", "error", verifErr)
			}
		}
		response += formatVerificationBlock(verif)

		return DelegateResult{
			Agent:    args.Agent,
			Response: response,
			Duration: duration.String(),
			EventID:  event.EventID,
		}, nil
	}

	t, err := functiontool.New(functiontool.Config{
		Name: "delegate_to_agent",
		Description: `Delegate a task to a specialist agent. You MUST use this tool for ALL delegations.
Before calling, provide your reasoning chain explaining why this agent was chosen.
Available agents: postgres_database_agent (database issues), k8s_agent (Kubernetes issues),
incident_agent (incident bundles), research_agent (web search for current info).`,
	}, delegateFunc)
	if err != nil {
		return nil, nil, err
	}
	return t, guard, nil
}

// callAgentWithTrace sends a message to an A2A agent with trace_id in metadata.
func callAgentWithTrace(ctx context.Context, agentURL, message, traceID string) (string, error) {
	// Fetch agent card
	cardURL := strings.TrimSuffix(agentURL, "/") + "/.well-known/agent-card.json"
	card, err := fetchAgentCard(ctx, cardURL)
	if err != nil {
		return "", fmt.Errorf("fetching agent card: %w", err)
	}

	// Override the URL in the card with our known-good URL.
	// This handles cases where agents advertise K8s service names (e.g., database-agent:1100)
	// but we're connecting via localhost.
	correctURL := strings.TrimSuffix(agentURL, "/") + "/invoke"
	if card.URL != correctURL {
		slog.Debug("overriding agent card URL", "original", card.URL, "override", correctURL)
		card.URL = correctURL
	}

	// Create A2A client
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		return "", fmt.Errorf("creating A2A client: %w", err)
	}

	// Send message with trace_id and principal/purpose in metadata so
	// downstream agents can enforce policy on behalf of the original caller.
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: message})
	meta := map[string]any{}
	if traceID != "" {
		meta["trace_id"] = traceID
	}
	if tc := TraceContextFromContext(ctx); tc != nil {
		if tc.Principal.UserID != "" {
			meta["user_id"] = tc.Principal.UserID
		}
		if len(tc.Principal.Roles) > 0 {
			meta["roles"] = tc.Principal.Roles
		}
		if tc.Principal.Service != "" {
			meta["service"] = tc.Principal.Service
		}
		if tc.Principal.AuthMethod != "" {
			meta["auth_method"] = tc.Principal.AuthMethod
		}
		if tc.Purpose != "" {
			meta["purpose"] = tc.Purpose
		}
		if tc.PurposeNote != "" {
			meta["purpose_note"] = tc.PurposeNote
		}
		if tc.PurposeExplicit {
			meta["purpose_explicit"] = true
		}
	}
	if len(meta) > 0 {
		msg.Metadata = meta
	}
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

// buildDelegationVerification queries the audit trail for tool_execution events
// belonging to this delegation (same traceID, after the delegation start time)
// and returns a DelegationVerification recording what was actually executed.
// It retries once after 200 ms to absorb async write propagation from RemoteStore.
func buildDelegationVerification(auditURL, traceID string, since time.Time, actionClass ActionClass, delegationEventID, agent string) *DelegationVerification {
	verif := &DelegationVerification{
		DelegationEventID: delegationEventID,
		Agent:             agent,
		ActionClass:       actionClass,
	}
	if auditURL == "" {
		return verif
	}

	events := fetchToolExecutionEvents(auditURL, traceID, since)
	for _, ev := range events {
		if ev.Tool == nil || ev.Tool.Name == "" {
			continue
		}
		name := ev.Tool.Name
		verif.ToolsConfirmed = append(verif.ToolsConfirmed, name)
		switch ClassifyTool(name) {
		case ActionDestructive:
			verif.DestructiveConfirmed = append(verif.DestructiveConfirmed, name)
		case ActionWrite:
			verif.WriteConfirmed = append(verif.WriteConfirmed, name)
		}
	}

	// Mismatch: delegation expected a write-or-stronger action but audit trail has none.
	// A destructive tool satisfies a write delegation (destructive ⊇ write).
	switch actionClass {
	case ActionDestructive:
		verif.Mismatch = len(verif.DestructiveConfirmed) == 0
	case ActionWrite:
		verif.Mismatch = len(verif.WriteConfirmed) == 0 && len(verif.DestructiveConfirmed) == 0
	}
	return verif
}

// fetchToolExecutionEvents queries auditd for tool_execution events in the given
// trace after a start time. Retries once after 200 ms for async propagation.
func fetchToolExecutionEvents(auditURL, traceID string, since time.Time) []Event {
	reqURL := strings.TrimRight(auditURL, "/") +
		"/v1/events?event_type=tool_execution&trace_id=" + traceID +
		"&since=" + since.UTC().Format(time.RFC3339)

	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		resp, err := http.Get(reqURL) //nolint:noctx
		if err != nil {
			slog.Debug("delegation verification: fetch failed", "attempt", attempt, "err", err)
			continue
		}
		var events []Event
		decodeErr := json.NewDecoder(resp.Body).Decode(&events)
		resp.Body.Close()
		if decodeErr != nil {
			slog.Debug("delegation verification: decode failed", "attempt", attempt, "err", decodeErr)
			continue
		}
		return events
	}
	return nil
}

// formatVerificationBlock builds the [AUDIT VERIFICATION] text appended to the
// DelegateResult.Response. The orchestrator LLM reads this and must use it as
// ground truth when formulating its reply.
func formatVerificationBlock(v *DelegationVerification) string {
	var sb strings.Builder
	sb.WriteString("\n\n---[AUDIT VERIFICATION | delegation: ")
	sb.WriteString(v.DelegationEventID)
	sb.WriteString("]\n")

	if len(v.ToolsConfirmed) == 0 {
		sb.WriteString("Tools confirmed by audit trail: none\n")
	} else {
		parts := make([]string, len(v.ToolsConfirmed))
		for i, t := range v.ToolsConfirmed {
			parts[i] = t + " (" + string(ClassifyTool(t)) + ")"
		}
		sb.WriteString("Tools confirmed by audit trail: " + strings.Join(parts, ", ") + "\n")
	}

	if len(v.WriteConfirmed) > 0 {
		sb.WriteString("Write tools confirmed: " + strings.Join(v.WriteConfirmed, ", ") + "\n")
	} else {
		sb.WriteString("Write tools confirmed: none\n")
	}
	if len(v.DestructiveConfirmed) > 0 {
		sb.WriteString("Destructive tools confirmed: " + strings.Join(v.DestructiveConfirmed, ", ") + "\n")
	} else {
		sb.WriteString("Destructive tools confirmed: none\n")
	}

	if v.Mismatch {
		sb.WriteString("⚠️  MISMATCH: this delegation was classified as " + string(v.ActionClass) + " but NO " + string(v.ActionClass) + "-or-stronger tool execution appears in the audit trail.\n")
		sb.WriteString("You MUST tell the user the action could not be verified and was likely not executed. Do NOT claim success.\n")
	} else {
		sb.WriteString("✓ VERIFICATION CLEAN: no mismatch. Tool execution matches delegation. Report the agent's result as-is (success or error).\n")
	}
	sb.WriteString("---")
	return sb.String()
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

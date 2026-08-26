package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
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
	Agent string `json:"agent" jsonschema:"Name of the agent to delegate to (e.g. postgres_database_agent, k8s_agent, sysadmin_agent, incident_agent, or research_agent)"`

	// RequestCategory classifies the type of request.
	RequestCategory string `json:"request_category" jsonschema:"Category of the request: database, kubernetes, sysadmin, incident, or research"`

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
// sessionPurpose, if non-empty, is injected as an explicit purpose into every
// delegation (equivalent to X-Purpose on API requests).
// It also creates and returns a DelegationGuard shared with NoDelegationCallback.
func DelegateTool(auditor Auditor, auditURL, auditAPIKey string, registry *AgentRegistry, sessionID, userID, callerName, sessionPurpose string) (tool.Tool, *DelegationGuard, error) {
	return DelegateToolWithTrace(auditor, auditURL, auditAPIKey, registry, sessionID, userID, "", callerName, sessionPurpose)
}

// DelegateToolWithTrace creates the delegate_to_agent tool with audit logging and trace ID.
// The returned DelegationGuard must be passed to NoDelegationCallback so the callback
// can detect invocations where delegate_to_agent was not called.
// sessionPurpose, if non-empty, is injected as an explicit purpose into every
// delegation (equivalent to X-Purpose on API requests).
func DelegateToolWithTrace(auditor Auditor, auditURL, auditAPIKey string, registry *AgentRegistry, sessionID, userID, traceID, callerName, sessionPurpose string) (tool.Tool, *DelegationGuard, error) {
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
						alts[i] = Alternative(a)
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
				_ = auditor.RecordOutcome(context.Background(), event.EventID, outcome)
			}
			return DelegateResult{
				Agent:    args.Agent,
				Response: fmt.Sprintf("Error: agent %q is not available. Available agents: %v", args.Agent, registry.List()),
				Duration: time.Since(start).String(),
				EventID:  event.EventID,
			}, nil
		}

		// Build the call context: inject session-level purpose (if set) so
		// callAgentWithTrace includes it in the A2A metadata for all delegations.
		callCtx := context.Background()
		if sessionPurpose != "" {
			callCtx = WithTraceContext(callCtx, &TraceContext{
				TraceID:         traceID,
				Purpose:         sessionPurpose,
				PurposeExplicit: true,
			})
		}

		// Call the agent via A2A
		slog.Debug("calling agent via A2A",
			"agent", args.Agent,
			"url", agentURL,
			"message", args.Message,
			"trace_id", traceID)
		response, err := callAgentWithTrace(callCtx, agentURL, args.Message, traceID)
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
			_ = auditor.RecordOutcome(context.Background(), event.EventID, outcome)
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
		verif := buildDelegationVerification(auditURL, auditAPIKey, traceID, start, actionClass, event.EventID, args.Agent, response)
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

// BuildDelegationVerification queries the audit trail for tool_execution events
// belonging to this delegation (same traceID, after the delegation start time)
// and returns a DelegationVerification recording what was actually executed.
// It retries once after 200 ms to absorb async write propagation from RemoteStore.
// Exported so the gateway can use it without duplicating the fetch logic.
// Pass apiKey="" when auditd does not require authentication. responseText is
// the agent's raw response text for this delegation — pass "" if unavailable;
// it is only used to check for a corroborated decline (see declinedActionSignal).
func BuildDelegationVerification(auditURL, auditAPIKey, traceID string, since time.Time, actionClass ActionClass, delegationEventID, agent, responseText string) *DelegationVerification {
	return buildDelegationVerification(auditURL, auditAPIKey, traceID, since, actionClass, delegationEventID, agent, responseText)
}

// buildDelegationVerification is the unexported implementation.
func buildDelegationVerification(auditURL, auditAPIKey, traceID string, since time.Time, actionClass ActionClass, delegationEventID, agent, responseText string) *DelegationVerification {
	verif := &DelegationVerification{
		DelegationEventID: delegationEventID,
		Agent:             agent,
		ActionClass:       actionClass,
	}
	if auditURL == "" {
		return verif
	}

	events := fetchToolExecutionEvents(auditURL, auditAPIKey, traceID, since)
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

	// policyEvents is fetched lazily and shared by both corroboration checks
	// below — only one HTTP round trip even when both would otherwise want it.
	var policyEvents []Event
	var policyEventsFetched bool
	fetchPolicyEventsOnce := func() []Event {
		if !policyEventsFetched {
			policyEvents = fetchPolicyDecisionEvents(auditURL, auditAPIKey, traceID, since)
			policyEventsFetched = true
		}
		return policyEvents
	}

	// Corroborated decline (self-reported): a write/destructive mismatch can
	// be a genuine, correct decision (the agent looked, found nothing to
	// write, and handed off) rather than a silent failure.
	// declinedActionSignal requires two independent structured protocol
	// lines to agree — ACTION_TAKEN: none AND a well-formed
	// ESCALATE_TO/TRANSITION_TO line — which is a materially stronger bar
	// than either alone; a genuinely broken/silently-failing call is
	// unlikely to also emit a clean, well-formed handoff line. Still
	// self-reported text, not audit-confirmed, so this is deliberately
	// paired with (not a replacement for) the unconditional check above.
	if verif.Mismatch && declinedActionSignal(responseText) {
		verif.Mismatch = false
		verif.MismatchReason = "no write/destructive tool executed, and the agent's own ACTION_TAKEN/handoff lines are consistent with a genuine decline (escalated/transitioned instead of writing), not a silent failure"
	}

	// Corroborated decline (policy-denied): the write/destructive action was
	// genuinely attempted and blocked by the policy engine — a real,
	// code-verified fact from the audit trail, not the model's self-report,
	// so a single signal is sufficient corroboration here (unlike
	// declinedActionSignal above, which needs two self-reported signals to
	// agree because either alone could be fabricated). Found live: a
	// terminal hop whose only write attempt is denied by policy (e.g.
	// restart_container blocked by a diagnostic-purpose policy) has nothing
	// to hand off to, so it can never satisfy declinedActionSignal's
	// handoff-line requirement, even though the audit trail already proves
	// nothing was silently skipped. hasActionClassDenial requires the
	// denied Action to match this delegation's own class (destructive
	// satisfies write, mirroring the "destructive ⊇ write" rule above) —
	// deliberately stricter than hasPolicyDenial below, which is fine
	// suppressing the lower-severity narrated-not-confirmed signal but too
	// permissive (any unrelated denial in the trace) for downgrading this
	// higher-severity mismatch.
	if verif.Mismatch && hasActionClassDenial(fetchPolicyEventsOnce(), actionClass) {
		verif.Mismatch = false
		verif.MismatchReason = "no write/destructive tool executed, but the audit trail shows the matching write/destructive action was attempted and denied by policy — a genuine, code-verified block, not a silent failure"
	}

	// Narrated-but-unconfirmed tool calls: orthogonal to the write/destructive-only
	// switch above, and unconditional on actionClass — a read delegation whose model
	// narrated calling a tool that never produced a tool_execution event is just as
	// untrustworthy as a write/destructive delegation with no evidence, since reads
	// are the bulk of actual triage/diagnosis work. Suppressed when a policy denial
	// explains the absence, so this doesn't flag policy working correctly as fabrication.
	reasoningEvents := fetchAgentReasoningEvents(auditURL, auditAPIKey, traceID, since)
	notConfirmed := narratedToolsNotConfirmed(reasoningEvents, verif.ToolsConfirmed)
	if len(notConfirmed) > 0 {
		if !hasPolicyDenial(fetchPolicyEventsOnce()) {
			verif.NarratedNotConfirmed = notConfirmed
			verif.Mismatch = true
		}
	}
	return verif
}

// actionTakenNoneRe matches an ACTION_TAKEN line whose value indicates
// nothing was done (e.g. "ACTION_TAKEN: none — escalation recommended").
// Matches the same line format cmd/gateway/playbooks.go's parseDiagnosticReport
// parses (leading "ACTION_TAKEN:", optional markdown bold, case-insensitive).
var actionTakenNoneRe = regexp.MustCompile(`(?im)^\**ACTION_TAKEN:\**\s*none\b`)

// escalationHandoffRe matches a well-formed ESCALATE_TO/TRANSITION_TO line
// whose target is not "none" — i.e. the model actually signaled a handoff,
// not just the absence of one. Matches the same line format
// cmd/gateway/playbooks.go's parseAgentEscalation parses.
var escalationHandoffRe = regexp.MustCompile(`(?im)^\**(?:ESCALATE_TO|TRANSITION_TO):\**\s*(\S+)`)

// declinedActionSignal reports whether responseText contains BOTH an
// ACTION_TAKEN line whose value indicates nothing was done AND a
// well-formed ESCALATE_TO/TRANSITION_TO handoff line — the same two
// protocol lines cmd/gateway/playbooks.go already parses for chaining.
// Requiring both together is a materially stronger bar than either alone: a
// genuinely broken/silently-failing call is unlikely to also emit a clean,
// well-formed handoff line. Still a self-reported signal (not confirmed by
// tool-execution audit events), so callers pair it with, rather than
// substitute it for, code-derived checks.
func declinedActionSignal(responseText string) bool {
	if responseText == "" {
		return false
	}
	if !actionTakenNoneRe.MatchString(responseText) {
		return false
	}
	m := escalationHandoffRe.FindStringSubmatch(responseText)
	if m == nil {
		return false
	}
	return strings.ToLower(m[1]) != "none"
}

// fetchToolExecutionEvents queries auditd for tool_execution events in the given
// trace after a start time. Retries once after 200 ms for async propagation.
// FetchToolExecutionEvents retrieves tool_execution audit events for the given
// trace from auditd. Returns nil when auditURL is empty or the fetch fails.
// Exported so the gateway can use it for post-run target-scope verification.
func FetchToolExecutionEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchToolExecutionEvents(auditURL, apiKey, traceID, since)
}

func fetchToolExecutionEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchEventsByType(auditURL, apiKey, traceID, "tool_execution", since, true)
}

// fetchAgentReasoningEvents queries auditd for agent_reasoning events in the given
// trace — used to cross-reference tools the model's own reasoning says it invoked
// against what actually executed. No retry: unlike tool_execution events (written by
// the mutation tool itself, sometimes racing the verification read), agent_reasoning
// events are written by the sub-agent's AfterModelCallback well before its response
// even returns to the caller running this check, so async propagation is not a
// realistic concern here — retrying would only add latency to every delegation.
func fetchAgentReasoningEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchEventsByType(auditURL, apiKey, traceID, "agent_reasoning", since, false)
}

// fetchPolicyDecisionEvents queries auditd for policy_decision events in the given
// trace — used to suppress a narrated-but-unconfirmed tool call when it was actually
// a legitimate policy denial rather than fabrication. No retry, same reasoning as
// fetchAgentReasoningEvents.
func fetchPolicyDecisionEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchEventsByType(auditURL, apiKey, traceID, "policy_decision", since, false)
}

// FetchPolicyDecisionEvents queries auditd for policy_decision events in the
// given trace. Exported so the gateway can surface policy denials on the
// playbook-run response, mirroring FetchToolExecutionEvents/
// FetchObjectiveEvidenceEvents above.
func FetchPolicyDecisionEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchPolicyDecisionEvents(auditURL, apiKey, traceID, since)
}

// FetchDelegationVerificationEvents queries auditd for delegation_verification
// events in the given trace — used by checkFabricationRisk (cmd/gateway/playbooks.go)
// to surface Mismatch/NarratedNotConfirmed on the live response. These events are
// otherwise only ever recorded durably (proxyToAgentWithTool sets the
// X-Audit-Mismatch response header as a same-request side channel, but that
// carries no detail and is never read by the playbook-run path at all — a
// caller had no way to see fabrication risk short of querying the audit
// trail directly). No retry, same reasoning as fetchAgentReasoningEvents/
// fetchPolicyDecisionEvents.
func FetchDelegationVerificationEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchEventsByType(auditURL, apiKey, traceID, "delegation_verification", since, false)
}

// FetchObjectiveEvidenceEvents queries auditd for objective_evidence events in
// the given trace — used by objectiveEvidenceForceGate (cmd/gateway/playbooks.go)
// to force a human-reviewed gate based on deterministic, code-derived tool
// evidence rather than the model's self-reported confidence. No retry, same
// reasoning as fetchAgentReasoningEvents/fetchPolicyDecisionEvents — these are
// recorded by the agent synchronously during its own tool call, not subject to
// the async write-propagation lag that motivates the tool_execution retry.
// Exported so the gateway can use it, mirroring FetchToolExecutionEvents above.
func FetchObjectiveEvidenceEvents(auditURL, apiKey, traceID string, since time.Time) []Event {
	return fetchEventsByType(auditURL, apiKey, traceID, "objective_evidence", since, false)
}

// fetchEventsByType queries auditd for events of a single type in the given trace
// after a start time. When retry is true, retries once after 200 ms to absorb async
// write propagation; when false, a single attempt is made and failures fail open
// (returns nil, treated by callers as "no events of this type").
func fetchEventsByType(auditURL, apiKey, traceID, eventType string, since time.Time, retry bool) []Event {
	reqURL := strings.TrimRight(auditURL, "/") +
		"/v1/events?event_type=" + eventType + "&trace_id=" + traceID +
		"&since=" + since.UTC().Format(time.RFC3339)

	attempts := 1
	if retry {
		attempts = 2
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodGet, reqURL, nil) //nolint:noctx
		if err != nil {
			slog.Debug("delegation verification: build request failed", "event_type", eventType, "attempt", attempt, "err", err)
			continue
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Debug("delegation verification: fetch failed", "event_type", eventType, "attempt", attempt, "err", err)
			continue
		}
		var events []Event
		decodeErr := json.NewDecoder(resp.Body).Decode(&events)
		resp.Body.Close()
		if decodeErr != nil {
			slog.Debug("delegation verification: decode failed", "event_type", eventType, "attempt", attempt, "err", decodeErr)
			continue
		}
		return events
	}
	return nil
}

// narratedToolsNotConfirmed returns the tool names the model's own reasoning says it
// invoked (AgentReasoning.ToolCalls — structured FunctionCall data, not text-scanned,
// so this cannot misfire on the model merely mentioning a tool name in prose) that
// have no matching entry in toolsConfirmed (names that actually produced a
// tool_execution event). A non-empty result means the model narrated calling a tool
// that never actually executed — either fabrication, or a legitimate policy denial /
// unregistered tool name; callers should check hasPolicyDenial before treating this
// as a mismatch.
func narratedToolsNotConfirmed(agentReasoningEvents []Event, toolsConfirmed []string) []string {
	confirmed := make(map[string]bool, len(toolsConfirmed))
	for _, name := range toolsConfirmed {
		confirmed[name] = true
	}

	seen := make(map[string]bool)
	var notConfirmed []string
	for _, ev := range agentReasoningEvents {
		if ev.AgentReasoning == nil {
			continue
		}
		for _, name := range ev.AgentReasoning.ToolCalls {
			if name == "" || seen[name] || confirmed[name] {
				continue
			}
			seen[name] = true
			notConfirmed = append(notConfirmed, name)
		}
	}
	return notConfirmed
}

// hasPolicyDenial returns true when any policy_decision event in the trace has
// effect=deny. Coarse-grained by design: any denial in the hop suppresses the
// narrated-tool-call mismatch check for that hop, rather than matching denials to
// specific tool names. Simpler and safer to ship correctly than precise per-tool
// matching, at the cost of occasionally under-reporting a real narration issue that
// happens to co-occur with an unrelated denial in the same hop — an acceptable
// tradeoff to avoid false-positive CRITICAL alerts on a brand-new check.
func hasPolicyDenial(policyDecisionEvents []Event) bool {
	for _, ev := range policyDecisionEvents {
		if ev.PolicyDecision != nil && ev.PolicyDecision.Effect == "deny" {
			return true
		}
	}
	return false
}

// hasActionClassDenial reports whether policyDecisionEvents contains a deny
// decision whose Action matches actionClass or is at least as strong
// (destructive satisfies a write delegation, mirroring the write/destructive-
// absence check's own "destructive ⊇ write" rule) — i.e. the audit trail
// shows the specific class of action this delegation needed was genuinely
// attempted and blocked, not just that some unrelated denial happened
// somewhere in the trace. Deliberately stricter than hasPolicyDenial above:
// that coarser check is fine for suppressing the lower-severity
// narrated-not-confirmed signal, but too permissive for downgrading the
// higher-severity write/destructive-absence mismatch.
func hasActionClassDenial(policyDecisionEvents []Event, actionClass ActionClass) bool {
	for _, ev := range policyDecisionEvents {
		if ev.PolicyDecision == nil || ev.PolicyDecision.Effect != "deny" {
			continue
		}
		switch actionClass {
		case ActionDestructive:
			if ev.PolicyDecision.Action == "destructive" {
				return true
			}
		case ActionWrite:
			if ev.PolicyDecision.Action == "write" || ev.PolicyDecision.Action == "destructive" {
				return true
			}
		}
	}
	return false
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

	if len(v.NarratedNotConfirmed) > 0 {
		sb.WriteString("⚠️  NARRATED BUT NOT CONFIRMED: the response describes calling " +
			strings.Join(v.NarratedNotConfirmed, ", ") + ", but no matching tool execution appears in the audit trail.\n")
		sb.WriteString("You MUST tell the user this could not be verified and may not have actually happened. Do NOT present it as a completed step.\n")
	} else if v.Mismatch {
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

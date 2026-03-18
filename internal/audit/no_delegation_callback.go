package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

const (
	// CorrectionToolName is the internal pseudo-tool injected by NoDelegationCallback
	// when the LLM responds without calling delegate_to_agent. It is registered in
	// the agent's tools list so handleFunctionCalls can resolve it, but its description
	// instructs the LLM not to call it directly.
	CorrectionToolName = "_delegation_required"

	// DefaultMaxDelegationRetries is the number of correction injections before
	// giving up and returning a suppressed-response message to the user.
	DefaultMaxDelegationRetries = 2
)

// correctionArgs are the arguments for the internal correction tool.
type correctionArgs struct {
	Attempt int `json:"attempt" jsonschema:"Which correction attempt this is (1-based)"`
}

// NoDelegationCorrectionTool returns the internal correction pseudo-tool that
// NoDelegationCallback injects when the LLM skips delegation. Register this tool
// in the agent's Tools list alongside delegate_to_agent.
//
// The tool emits a no_delegation_turn audit event and returns a correction
// message that appears in the LLM's context for the next iteration, prompting
// it to call delegate_to_agent.
func NoDelegationCorrectionTool(auditor Auditor, sessionID string) (tool.Tool, error) {
	fn := func(ctx tool.Context, args correctionArgs) (map[string]any, error) {
		invID := ctx.InvocationID()

		slog.Warn("no_delegation_turn: correction injected",
			"invocation_id", invID,
			"attempt", args.Attempt)

		// Emit audit event so the no-delegation turn is visible in the audit trail.
		if auditor != nil {
			ev := &Event{
				EventID:   "nd_" + uuid.New().String()[:8],
				Timestamp: time.Now().UTC(),
				EventType: EventTypeNoDelegationTurn,
				Session:   Session{ID: sessionID},
				Output: &Output{
					Response: fmt.Sprintf("correction attempt %d", args.Attempt),
				},
			}
			if err := auditor.Record(context.Background(), ev); err != nil {
				slog.Warn("failed to record no_delegation_turn event", "error", err)
			}
		}

		msg := fmt.Sprintf(
			"[SYSTEM CORRECTION — attempt %d] "+
				"You responded directly without calling delegate_to_agent. "+
				"You MUST delegate ALL technical requests to a specialist agent using the delegate_to_agent tool. "+
				"Do not answer infrastructure, database, or Kubernetes questions directly. "+
				"Call delegate_to_agent now.",
			args.Attempt,
		)
		return map[string]any{"correction": msg}, nil
	}

	return functiontool.New(functiontool.Config{
		Name: CorrectionToolName,
		Description: "INTERNAL SYSTEM TOOL — do not call this tool. " +
			"It is injected automatically when a correction is required.",
	}, fn)
}

// NoDelegationCallback returns an AfterModelCallback that detects when the
// orchestrator LLM returns a text-only response without having called
// delegate_to_agent in the current invocation.
//
// On detection it injects a correction by returning a synthetic *model.LLMResponse
// containing a function call for CorrectionToolName. The ADK executes the tool,
// adds the correction message to the LLM's context, and calls the LLM again —
// giving it a chance to delegate properly. This repeats up to maxRetries times.
//
// After maxRetries exhaustion, a suppressed-response message is returned to the
// user so they can retry, rather than silently delivering the fabricated answer.
//
// Pass maxRetries <= 0 to use DefaultMaxDelegationRetries.
func NoDelegationCallback(guard *DelegationGuard, maxRetries int) func(agent.CallbackContext, *adkmodel.LLMResponse, error) (*adkmodel.LLMResponse, error) {
	if maxRetries <= 0 {
		maxRetries = DefaultMaxDelegationRetries
	}

	return func(ctx agent.CallbackContext, llmResponse *adkmodel.LLMResponse, llmErr error) (*adkmodel.LLMResponse, error) {
		if llmResponse == nil || llmResponse.Content == nil {
			return nil, nil
		}

		invID := ctx.InvocationID()

		// If delegate_to_agent was called during this invocation, the text
		// response is a legitimate summary. Nothing to correct.
		if guard.WasCalled(invID) {
			return nil, nil
		}

		// Determine if this is a text-only (final) response — function-call
		// responses don't need correction; they will be processed normally.
		hasText := false
		hasFunctionCall := false
		for _, part := range llmResponse.Content.Parts {
			if part.Text != "" && !part.Thought {
				hasText = true
			}
			if part.FunctionCall != nil {
				hasFunctionCall = true
			}
		}

		// If the LLM is about to call a tool (including delegate_to_agent on
		// this step), don't interfere.
		if hasFunctionCall || !hasText {
			return nil, nil
		}

		// Text-only response without delegation detected.
		attempt := guard.IncrementRetry(invID)

		if attempt > maxRetries {
			// Max retries exhausted — return a suppressed-response message.
			slog.Warn("no_delegation_turn: max retries exhausted, suppressing response",
				"invocation_id", invID,
				"attempts", attempt)

			suppressed := &adkmodel.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{
							Text: "I was unable to route this request through the appropriate specialist agent after " +
								fmt.Sprintf("%d", maxRetries) +
								" attempts. Please try rephrasing your question or contact support.",
						},
					},
				},
			}
			return suppressed, nil
		}

		slog.Info("no_delegation_turn: injecting correction",
			"invocation_id", invID,
			"attempt", attempt,
			"max_retries", maxRetries)

		// Return a synthetic response containing a function call for the
		// correction pseudo-tool. The ADK will execute it, add its output to
		// the LLM's context, and call the LLM again.
		injected := &adkmodel.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{
					{
						FunctionCall: &genai.FunctionCall{
							Name: CorrectionToolName,
							Args: map[string]any{"attempt": attempt},
						},
					},
				},
			},
		}
		return injected, nil
	}
}

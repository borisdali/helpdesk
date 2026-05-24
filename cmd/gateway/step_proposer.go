package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/identity"
)

// StepProposal is the structured output of the re-planning LLM for one action.
type StepProposal struct {
	Index       int            `json:"index"`
	Agent       string         `json:"agent"`
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args"`
	Reason      string         `json:"reason,omitempty"`
	ActionClass string         `json:"action_class,omitempty"` // "read", "write", "destructive"
}

// stepProposerResult is the raw LLM output before index assignment.
type stepProposerResult struct {
	Action  string         `json:"action"`  // "execute_step" | "complete"
	Agent   string         `json:"agent"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Reason  string         `json:"reason"`
	Summary string         `json:"summary"` // when action=complete
}

const stepProposerPromptTemplate = `You are executing a database remediation playbook step-by-step.

PLAYBOOK: %s
GUIDANCE:
%s

TARGET: %s

TOOL CALLS EXECUTED SO FAR (in order):
%s

Based on the guidance above and the tool results so far, what is the SINGLE next tool call to make?
Do NOT confuse the numbered tool calls above with the numbered steps in the guidance — they are independent.
Determine what the guidance still requires that has not yet been done, and propose the next tool call for it.

Only return action=complete when ALL goals in the guidance have been achieved (including any required terminations and verification).

Respond with JSON only, no other text:
{
  "action": "execute_step",
  "agent": "database",
  "tool": "<tool_name>",
  "args": {},
  "reason": "<one sentence shown to the operator explaining why>"
}
OR when done:
{
  "action": "complete",
  "summary": "<brief summary of what was accomplished>"
}`

// proposeNextStep calls the planner LLM to propose the single next remediation
// action given the playbook guidance and history of already-executed steps.
// Returns (proposal, isDone, summary, error).
func (g *Gateway) proposeNextStep(ctx context.Context, pb *audit.Playbook, connStr string, history []*audit.PlaybookRunStep) (*StepProposal, bool, string, error) {
	if g.plannerLLM == nil {
		return nil, false, "", fmt.Errorf("planner LLM not configured")
	}

	historyStr := buildHistorySection(history)
	prompt := fmt.Sprintf(stepProposerPromptTemplate,
		pb.Name,
		pb.Guidance,
		connStr,
		historyStr,
	)

	raw, err := g.plannerLLM(ctx, prompt)
	if err != nil {
		return nil, false, "", fmt.Errorf("step proposer LLM call failed: %w", err)
	}

	// Extract JSON block (model may wrap in markdown fences).
	jsonStr := extractFirstJSON(raw)

	var result stepProposerResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, false, "", fmt.Errorf("step proposer response parse failed: %w (raw: %.200s)", err, raw)
	}

	if result.Action == "complete" {
		return nil, true, result.Summary, nil
	}

	if result.Tool == "" {
		return nil, false, "", fmt.Errorf("step proposer returned empty tool name")
	}

	nextIdx := len(history) + 1
	actionClass := string(audit.ToolClassification[result.Tool])
	if actionClass == "" {
		actionClass = string(audit.ActionUnknown)
	}
	proposal := &StepProposal{
		Index:       nextIdx,
		Agent:       result.Agent,
		Tool:        result.Tool,
		Args:        result.Args,
		Reason:      result.Reason,
		ActionClass: actionClass,
	}
	if proposal.Agent == "" {
		proposal.Agent = "database"
	}
	if proposal.Args == nil {
		proposal.Args = map[string]any{}
	}

	slog.Info("step proposer: next action", "tool", proposal.Tool, "args", proposal.Args, "reason", proposal.Reason)
	return proposal, false, "", nil
}

func buildHistorySection(history []*audit.PlaybookRunStep) string {
	if len(history) == 0 {
		return "(none yet — this is the first tool call)"
	}
	var sb strings.Builder
	for _, s := range history {
		argsJSON, _ := json.Marshal(s.Args)
		sb.WriteString(fmt.Sprintf("Tool call #%d: %s(%s)\nResult: %s\n\n",
			s.StepIndex, s.Tool, string(argsJSON), s.Result))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func extractFirstJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j >= i {
			return s[i : j+1]
		}
	}
	return s
}

// callToolForStep calls an agent tool directly and returns its output string.
// Used by the agent_approve proceed handler to execute an approved step.
func (g *Gateway) callToolForStep(ctx context.Context, r *http.Request, traceID, purpose, agentName, toolName string, args map[string]any) (string, error) {
	principal, _, _, _, err := g.resolveRequest(r, "", "")
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}
	return g.callToolWithPrincipal(ctx, traceID, purpose, agentName, toolName, args, principal)
}

// callToolWithPrincipal is the low-level agent HTTP call used by callToolForStep.
func (g *Gateway) callToolWithPrincipal(ctx context.Context, traceID, purpose, agentName, toolName string, args map[string]any, principal identity.ResolvedPrincipal) (string, error) {
	if resolved, ok := agentAliases[agentName]; ok {
		agentName = resolved
	}
	agentInfo, ok := g.agents[agentName]
	if !ok {
		return "", fmt.Errorf("agent %q not available", agentName)
	}
	baseURL := strings.TrimSuffix(agentInfo.InvokeURL, "/invoke")

	reqBody := directToolReq{
		TraceID:   traceID,
		Principal: principal,
		Purpose:   purpose,
		Args:      args,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal tool request: %w", err)
	}

	toolURL := baseURL + "/tool/" + toolName
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, toolURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("build tool request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if g.agentAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.agentAPIKey)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	httpResp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tool call to %s/%s: %w", agentName, toolName, err)
	}
	defer httpResp.Body.Close()
	respBytes, _ := io.ReadAll(httpResp.Body)

	var toolResp directToolResp
	if jsonErr := json.Unmarshal(respBytes, &toolResp); jsonErr != nil {
		return string(respBytes), nil
	}
	if toolResp.Error != "" {
		return "", fmt.Errorf("%s", toolResp.Error)
	}
	return toolResp.Output, nil
}

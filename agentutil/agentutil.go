// Package agentutil provides the SDK surface for building helpdesk agents.
// It extracts the boilerplate duplicated across sub-agents: config loading,
// LLM creation, and A2A server startup.
package agentutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adka2a"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"

	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/identity"
	"helpdesk/internal/logging"
	"helpdesk/internal/model"
	"helpdesk/internal/policy"
)

// Config holds common agent configuration from HELPDESK_* env vars.
type Config struct {
	ModelVendor string
	ModelName   string
	APIKey      string
	ListenAddr  string
	ExternalURL string // Optional: externally reachable URL for the agent card

	// Audit configuration
	AuditEnabled bool
	AuditURL     string // URL of central audit service (preferred)
	AuditDir     string // Local directory for audit.db (fallback if AuditURL not set)
	AuditAPIKey  string // Bearer token for auditd service account (when auditd enforces auth)

	// Policy configuration
	PolicyEnabled bool   // Master switch — must be true to enforce policy
	PolicyFile    string // Path to policy YAML file (required when PolicyEnabled)
	PolicyDryRun  bool   // Log policy decisions but don't enforce
	DefaultPolicy string // "allow" or "deny" when no policy matches (default: "deny")

	// Inbound authentication for agent endpoints.
	// When set, POST /tool/{name} requires a valid service-account API key.
	// Uses the same users.yaml format as auditd (HELPDESK_USERS_FILE).
	UsersFile string

	// Approval configuration
	ApprovalEnabled bool          // Enable approval workflow
	ApprovalTimeout time.Duration // How long to wait for approval (default: 30s)

	// Remote policy check (set automatically from AuditURL when PolicyEnabled)
	PolicyCheckURL     string        // auditd base URL for /v1/governance/check; enables remote mode
	PolicyCheckTimeout time.Duration // HTTP timeout for remote checks (default 5s)
}

// MustLoadConfig reads env vars. defaultAddr is used when HELPDESK_AGENT_ADDR is unset.
// Exits the process if required vars (MODEL_VENDOR, MODEL_NAME, API_KEY) are missing.
// It also initialises structured logging via logging.InitLogging.
func MustLoadConfig(defaultAddr string) Config {
	logging.InitLogging(os.Args[1:])

	auditEnabled := os.Getenv("HELPDESK_AUDIT_ENABLED")
	policyEnabled := os.Getenv("HELPDESK_POLICY_ENABLED")
	policyFile := os.Getenv("HELPDESK_POLICY_FILE")
	policyDryRun := os.Getenv("HELPDESK_POLICY_DRY_RUN")
	approvalEnabled := os.Getenv("HELPDESK_APPROVAL_ENABLED")

	// Backward compat: if HELPDESK_POLICY_FILE is set without HELPDESK_POLICY_ENABLED,
	// infer enabled=true and warn so operators know to update their config.
	policyEnabledBool := policyEnabled == "true" || policyEnabled == "1"
	if !policyEnabledBool && policyFile != "" && policyEnabled == "" {
		slog.Warn("HELPDESK_POLICY_FILE is set without HELPDESK_POLICY_ENABLED; inferring enabled=true — set HELPDESK_POLICY_ENABLED=true explicitly")
		policyEnabledBool = true
	}

	// Parse approval timeout (default 30s)
	approvalTimeout := 30 * time.Second
	if v := os.Getenv("HELPDESK_APPROVAL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			approvalTimeout = d
		}
	}

	cfg := Config{
		ModelVendor:     os.Getenv("HELPDESK_MODEL_VENDOR"),
		ModelName:       os.Getenv("HELPDESK_MODEL_NAME"),
		APIKey:          os.Getenv("HELPDESK_API_KEY"),
		ListenAddr:      os.Getenv("HELPDESK_AGENT_ADDR"),
		ExternalURL:     os.Getenv("HELPDESK_AGENT_URL"),
		AuditEnabled:    auditEnabled == "true" || auditEnabled == "1",
		AuditURL:        os.Getenv("HELPDESK_AUDIT_URL"),
		AuditDir:        os.Getenv("HELPDESK_AUDIT_DIR"),
		AuditAPIKey:     os.Getenv("HELPDESK_AUDIT_API_KEY"),
		PolicyEnabled:   policyEnabledBool,
		PolicyFile:      policyFile,
		PolicyDryRun:    policyDryRun == "true" || policyDryRun == "1",
		DefaultPolicy:   os.Getenv("HELPDESK_DEFAULT_POLICY"),
		ApprovalEnabled: approvalEnabled == "true" || approvalEnabled == "1",
		ApprovalTimeout: approvalTimeout,
		UsersFile:       os.Getenv("HELPDESK_USERS_FILE"),
	}

	// Enable remote policy check mode: when HELPDESK_AUDIT_URL is set and policy is
	// enabled, delegate policy evaluation to auditd. Agents need no local engine copy.
	if cfg.PolicyEnabled && cfg.AuditURL != "" {
		cfg.PolicyCheckURL = cfg.AuditURL
	}

	if cfg.ModelVendor == "" || cfg.ModelName == "" || cfg.APIKey == "" {
		slog.Error("missing required environment variables: HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY")
		os.Exit(1)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = defaultAddr
	}

	if cfg.DefaultPolicy == "" {
		cfg.DefaultPolicy = "deny"
	}

	return cfg
}

// TextCompleter is a function that sends a single-turn text prompt to an LLM
// and returns the response text. Suitable for one-shot generation tasks like
// the fleet job planner that do not need a full agentic loop.
type TextCompleter func(ctx context.Context, prompt string) (string, error)

// NewTextCompleter creates a vendor-agnostic one-shot text completion function
// backed by whichever LLM vendor is configured in cfg.ModelVendor.
// The returned function is safe for concurrent use.
func NewTextCompleter(ctx context.Context, cfg Config) (TextCompleter, error) {
	llm, err := NewLLM(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, prompt string) (string, error) {
		req := &adkmodel.LLMRequest{
			Contents: []*genai.Content{
				{
					Role:  "user",
					Parts: []*genai.Part{{Text: prompt}},
				},
			},
		}
		var sb strings.Builder
		for resp, err := range llm.GenerateContent(ctx, req, false) {
			if err != nil {
				return "", err
			}
			if resp != nil && resp.Content != nil {
				for _, part := range resp.Content.Parts {
					sb.WriteString(part.Text)
				}
			}
		}
		return sb.String(), nil
	}, nil
}

// NewLLM creates an LLM model based on Config.ModelVendor (gemini or anthropic).
func NewLLM(ctx context.Context, cfg Config) (adkmodel.LLM, error) {
	switch strings.ToLower(cfg.ModelVendor) {
	case "google", "gemini":
		llm, err := gemini.NewModel(ctx, cfg.ModelName, &genai.ClientConfig{APIKey: cfg.APIKey})
		if err != nil {
			return nil, fmt.Errorf("failed to create Gemini model: %v", err)
		}
		slog.Info("using model", "vendor", "gemini", "model", cfg.ModelName)
		return llm, nil

	case "anthropic":
		llm, err := model.NewAnthropicModel(ctx, cfg.ModelName, cfg.APIKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create Anthropic model: %v", err)
		}
		slog.Info("using model", "vendor", "anthropic", "model", cfg.ModelName)
		return llm, nil

	default:
		return nil, fmt.Errorf("unknown model vendor: %s (supported: google, gemini, anthropic)", cfg.ModelVendor)
	}
}

// InitPolicyEngine initializes a policy engine for an agent if configured.
// Returns nil if policy enforcement is disabled.
func InitPolicyEngine(cfg Config) (*policy.Engine, error) {
	if !cfg.PolicyEnabled {
		slog.Debug("policy enforcement disabled (HELPDESK_POLICY_ENABLED not set)")
		return nil, nil
	}

	// Remote check mode: policy evaluation is delegated to auditd.
	// The PolicyEnforcer will call POST /v1/governance/check instead of a local engine.
	if cfg.PolicyCheckURL != "" {
		if probeRemotePolicyEngine(cfg.PolicyCheckURL, cfg.AuditAPIKey) {
			slog.Info("policy enforcement enabled (remote check mode)", "url", cfg.PolicyCheckURL)
		} else {
			slog.Warn("policy enforcement enabled (remote check mode) but remote has no policy engine",
				"url", cfg.PolicyCheckURL,
				"hint", "set HELPDESK_POLICY_FILE and HELPDESK_POLICY_ENABLED on the auditd server")
		}
		return nil, nil
	}

	if cfg.PolicyFile == "" {
		return nil, fmt.Errorf("HELPDESK_POLICY_ENABLED is true but HELPDESK_POLICY_FILE is not set")
	}

	// Load policies from file
	policyCfg, err := policy.LoadFile(cfg.PolicyFile)
	if err != nil {
		return nil, fmt.Errorf("load policy file: %w", err)
	}

	// Determine default effect
	defaultEffect := policy.EffectDeny
	if strings.ToLower(cfg.DefaultPolicy) == "allow" {
		defaultEffect = policy.EffectAllow
	}

	engine := policy.NewEngine(policy.EngineConfig{
		PolicyConfig:  policyCfg,
		DefaultEffect: defaultEffect,
		DryRun:        cfg.PolicyDryRun,
	})

	slog.Info("policy enforcement enabled",
		"file", cfg.PolicyFile,
		"policies", len(policyCfg.Policies),
		"dry_run", cfg.PolicyDryRun,
		"default", cfg.DefaultPolicy)

	return engine, nil
}

// PolicyEnforcer wraps a policy engine with convenience methods for agents.
type PolicyEnforcer struct {
	engine                     *policy.Engine
	policyCheckURL             string        // non-empty → remote check mode via auditd
	policyCheckAPIKey          string        // Bearer token for authenticating to auditd (HELPDESK_AUDIT_API_KEY)
	policyCheckTimeout         time.Duration // HTTP timeout for remote checks (default 5s)
	traceStore                 *audit.CurrentTraceStore
	approvalClient             *audit.ApprovalClient
	approvalTimeout            time.Duration
	agentName                  string
	toolAuditor                *audit.ToolAuditor // records policy decisions to the audit trail
	requirePurposeForSensitive bool               // enforce explicit purpose for pii/critical resources
}

// PolicyEnforcerConfig configures the policy enforcer.
type PolicyEnforcerConfig struct {
	Engine                     *policy.Engine
	PolicyCheckURL             string        // auditd base URL for remote checks (set from cfg.PolicyCheckURL)
	PolicyCheckAPIKey          string        // Bearer token for authenticating to auditd (HELPDESK_AUDIT_API_KEY)
	PolicyCheckTimeout         time.Duration // HTTP timeout for remote checks (default 5s)
	TraceStore                 *audit.CurrentTraceStore
	ApprovalClient             *audit.ApprovalClient
	ApprovalTimeout            time.Duration
	AgentName                  string
	ToolAuditor                *audit.ToolAuditor // optional; enables policy decision audit events
	RequirePurposeForSensitive bool               // deny access to pii/critical resources without explicit purpose
}

// NewPolicyEnforcer creates a policy enforcer. If engine is nil, enforcement is disabled.
func NewPolicyEnforcer(engine *policy.Engine, traceStore *audit.CurrentTraceStore) *PolicyEnforcer {
	return &PolicyEnforcer{
		engine:          engine,
		traceStore:      traceStore,
		approvalTimeout: 30 * time.Second,
	}
}

// NewPolicyEnforcerWithConfig creates a policy enforcer with full configuration.
func NewPolicyEnforcerWithConfig(cfg PolicyEnforcerConfig) *PolicyEnforcer {
	timeout := cfg.ApprovalTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	checkTimeout := cfg.PolicyCheckTimeout
	if checkTimeout == 0 {
		checkTimeout = 5 * time.Second
	}
	return &PolicyEnforcer{
		engine:                     cfg.Engine,
		policyCheckURL:             cfg.PolicyCheckURL,
		policyCheckAPIKey:          cfg.PolicyCheckAPIKey,
		policyCheckTimeout:         checkTimeout,
		traceStore:                 cfg.TraceStore,
		approvalClient:             cfg.ApprovalClient,
		toolAuditor:                cfg.ToolAuditor,
		approvalTimeout:            timeout,
		agentName:                  cfg.AgentName,
		requirePurposeForSensitive: cfg.RequirePurposeForSensitive,
	}
}

// CheckTool evaluates whether a tool execution is allowed.
// Returns nil if allowed, error if denied.
// If approval is required and an approval client is configured, it will request
// approval and wait for resolution.
// sensitivity is a list of sensitivity labels from the infra config (e.g., "pii", "critical").
func (e *PolicyEnforcer) CheckTool(ctx context.Context, resourceType, resourceName string, action policy.ActionClass, tags []string, note string, sensitivity []string) error {
	// Emit unconditional tool_invoked event before any policy evaluation.
	// Fires even when enforcement is disabled, so govbot can detect tool calls
	// that were never policy-checked (tool_invoked with no matching policy_decision).
	if e.toolAuditor != nil {
		e.toolAuditor.RecordToolInvoked(ctx, resourceType, resourceName, string(action), tags)
	}

	// Block write and destructive tools unconditionally in readonly-governed mode.
	// This is enforced in code, not by policy, so a misconfigured policy file
	// cannot accidentally permit a mutation.
	if strings.ToLower(os.Getenv("HELPDESK_OPERATING_MODE")) == "readonly-governed" &&
		(action == policy.ActionWrite || action == policy.ActionDestructive) {
		msg := fmt.Sprintf(
			"tool %q is a %s operation and is not permitted in readonly-governed mode; "+
				"set HELPDESK_OPERATING_MODE=fix to enable mutations",
			resourceType, string(action))
		if e.toolAuditor != nil {
			principal := audit.PrincipalFromContext(ctx)
			e.toolAuditor.RecordPolicyDecision(ctx, audit.PolicyDecision{
				ResourceType: resourceType,
				ResourceName: resourceName,
				Action:       string(action),
				Tags:         tags,
				Effect:       "deny",
				PolicyName:   "readonly_governed_mode",
				Message:      msg,
				Note:         note,
				UserID:       principal.UserID,
				Roles:        principal.Roles,
				Service:      principal.Service,
				AuthMethod:   principal.AuthMethod,
			})
		}
		return fmt.Errorf("%s", msg)
	}

	// Pre-check: if HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE is set and this resource
	// has pii or critical sensitivity, require an explicit purpose declaration.
	if e.requirePurposeForSensitive && hasSensitiveSensitivity(sensitivity) {
		if !audit.PurposeExplicitFromContext(ctx) {
			purpose, _ := audit.PurposeFromContext(ctx)
			denyMsg := fmt.Sprintf("access to %s/%s requires an explicit purpose declaration "+
				"(sensitivity: %s, current purpose %q was derived from operating mode, not declared); "+
				"add 'purpose' to your request body or X-Purpose header",
				resourceType, resourceName, strings.Join(sensitivity, ","), purpose)
			if e.toolAuditor != nil {
				principal := audit.PrincipalFromContext(ctx)
				e.toolAuditor.RecordPolicyDecision(ctx, audit.PolicyDecision{
					ResourceType: resourceType,
					ResourceName: resourceName,
					Action:       string(action),
					Tags:         tags,
					Effect:       "deny",
					PolicyName:   "require_purpose_for_sensitive",
					Message:      denyMsg,
					Note:         note,
					UserID:       principal.UserID,
					Roles:        principal.Roles,
					Service:      principal.Service,
					AuthMethod:   principal.AuthMethod,
				})
			}
			return fmt.Errorf("%s", denyMsg)
		}
	}

	if e.engine == nil && e.policyCheckURL == "" {
		return nil // No enforcement
	}

	// Remote check mode: delegate evaluation + audit recording to auditd atomically.
	if e.policyCheckURL != "" {
		traceID := ""
		if e.traceStore != nil {
			traceID = e.traceStore.Get()
		}
		principal := audit.PrincipalFromContext(ctx)
		purpose, purposeNote := audit.PurposeFromContext(ctx)
		toolName := toolNameFromContext(ctx)
		resp, err := e.callRemotePolicyCheck(ctx, policyCheckReq{
			ResourceType: resourceType,
			ResourceName: resourceName,
			Action:       string(action),
			Tags:         tags,
			TraceID:      traceID,
			AgentName:    e.agentName,
			Note:         note,
			Principal:    principal,
			Purpose:      purpose,
			PurposeNote:  purposeNote,
			Sensitivity:  sensitivity,
			ToolName:     toolName,
		})
		if err != nil {
			return err
		}
		return e.handleRemoteResponse(ctx, resp, traceID, resourceType, resourceName, action, tags, note)
	}

	// Local engine path.
	principal := audit.PrincipalFromContext(ctx)
	purpose, purposeNote := audit.PurposeFromContext(ctx)
	req := policy.Request{
		Principal: policy.RequestPrincipal{
			UserID:  principal.UserID,
			Roles:   principal.Roles,
			Service: principal.Service,
		},
		Resource: policy.RequestResource{
			Type:        resourceType,
			Name:        resourceName,
			Tags:        tags,
			Sensitivity: sensitivity,
			ToolName:    toolNameFromContext(ctx),
		},
		Action: action,
	}

	// Add trace context if available
	traceID := ""
	if e.traceStore != nil {
		traceID = e.traceStore.Get()
		req.Context.TraceID = traceID
	}
	req.Context.Purpose = purpose
	req.Context.PurposeNote = purposeNote

	trace := e.engine.Explain(req)
	decision := trace.Decision

	// Marshal the trace for the audit record (json.RawMessage avoids an import cycle
	// between the audit and policy packages).
	traceJSON, _ := json.Marshal(trace)

	// Record the policy decision to the audit trail (allow, deny, or require_approval).
	if e.toolAuditor != nil {
		e.toolAuditor.RecordPolicyDecision(ctx, audit.PolicyDecision{
			ResourceType: resourceType,
			ResourceName: resourceName,
			Action:       string(action),
			Tags:         tags,
			Effect:       string(decision.Effect),
			PolicyName:   decision.PolicyName,
			RuleIndex:    decision.RuleIndex,
			Message:      decision.Message,
			Note:         note,
			Trace:        traceJSON,
			Explanation:  trace.Explanation,
			UserID:       principal.UserID,
			Roles:        principal.Roles,
			Service:      principal.Service,
			AuthMethod:   principal.AuthMethod,
			Purpose:      purpose,
			PurposeNote:  purposeNote,
		})
	}

	// If approval is required, attempt to get approval.
	if decision.NeedsApproval() && e.approvalClient != nil {
		return e.requestApproval(ctx, traceID, resourceType, resourceName, action, tags, note)
	}
	if decision.NeedsApproval() {
		return &policy.ApprovalRequiredError{Decision: decision}
	}
	if decision.IsDenied() {
		return &policy.DeniedError{Decision: decision, Explanation: trace.Explanation}
	}
	return nil
}

// ApprovalPendingError is returned when an approval request has been created but
// not yet granted. The caller should surface the ApprovalID to the user so they
// can direct an approver to resolve it, then retry the operation.
type ApprovalPendingError struct {
	ApprovalID string
}

func (e *ApprovalPendingError) Error() string {
	return fmt.Sprintf(
		"approval required (ID: %s) — this operation needs human authorization before it can execute. "+
			"Ask an approver to run: ./approvals approve %s — then reply here to retry.",
		e.ApprovalID, e.ApprovalID)
}

// requestApproval creates or reuses an approval request and returns immediately.
// On first call it creates the request and returns ApprovalPendingError so the
// LLM can surface the approval ID to the user. On retry (next turn) it first
// checks for an approved approval by tool name (cross-turn lookup — trace IDs
// differ between turns), then falls through to the existing trace-based check.
// note carries optional free-text context for the approver.
func (e *PolicyEnforcer) requestApproval(ctx context.Context, traceID, resourceType, resourceName string, action policy.ActionClass, tags []string, note string) error {
	toolKey := resourceType + ":" + resourceName

	// Cross-turn lookup: check for an approved or pending approval by tool name.
	// The trace ID changes each request, so this is the reliable cross-turn path.
	// agentName scopes the lookup when set; empty agentName matches any agent.
	existing, err := e.approvalClient.FindApprovalByTool(ctx, toolKey, e.agentName)
	if err == nil && existing != nil {
		switch existing.Status {
		case "approved":
			slog.Info("using existing approval (cross-turn lookup)",
				"approval_id", existing.ApprovalID,
				"resource", toolKey)
			return nil
		case "pending":
			slog.Info("pending approval found (cross-turn lookup)",
				"approval_id", existing.ApprovalID,
				"resource", toolKey)
			return &ApprovalPendingError{ApprovalID: existing.ApprovalID}
		}
	}

	// Same-turn fallback: check for an existing valid approval by trace ID.
	if traceID != "" {
		existing, err := e.approvalClient.CheckExistingApproval(ctx, traceID, toolKey)
		if err == nil && existing != nil {
			slog.Info("using existing approval (trace-based lookup)",
				"approval_id", existing.ApprovalID,
				"trace_id", traceID,
				"resource", toolKey)
			return nil
		}
	}

	// Create a new approval request and return immediately with the pending ID.
	// Identify the human principal so the approval record carries the actual user,
	// not the agent process name. The four-eyes check at approval time compares
	// this value against the approver's identity.
	principal := audit.PrincipalFromContext(ctx)
	requestedBy := principal.UserID
	if requestedBy == "" {
		requestedBy = e.agentName // fallback for unauthenticated / service-account callers
	}

	reqCtx := map[string]any{"tags": tags}
	if note != "" {
		reqCtx["session_info"] = note
	}
	createResp, err := e.approvalClient.CreateApproval(ctx, audit.ApprovalCreateRequest{
		TraceID:      traceID,
		ActionClass:  string(action),
		ToolName:     toolKey,
		AgentName:    e.agentName,
		ResourceType: resourceType,
		ResourceName: resourceName,
		RequestedBy:  requestedBy,
		Context:      reqCtx,
	})
	if err != nil {
		return fmt.Errorf("approval request failed: %w", err)
	}

	slog.Info("approval request created",
		"approval_id", createResp.ApprovalID,
		"resource", toolKey)
	return &ApprovalPendingError{ApprovalID: createResp.ApprovalID}
}

// CheckDatabase is a convenience method for database operations.
// sensitivity is a list of sensitivity labels from the infra config (e.g., "pii", "critical").
func (e *PolicyEnforcer) CheckDatabase(ctx context.Context, dbName string, action policy.ActionClass, tags []string, note string, sensitivity []string) error {
	return e.CheckTool(ctx, "database", dbName, action, tags, note, sensitivity)
}

// CheckKubernetes is a convenience method for Kubernetes operations.
// sensitivity is a list of sensitivity labels from the infra config (e.g., "pii", "critical").
func (e *PolicyEnforcer) CheckKubernetes(ctx context.Context, namespace string, action policy.ActionClass, tags []string, note string, sensitivity []string) error {
	return e.CheckTool(ctx, "kubernetes", namespace, action, tags, note, sensitivity)
}

// hasSensitiveSensitivity returns true if any sensitivity label is "pii" or "critical".
func hasSensitiveSensitivity(sensitivity []string) bool {
	for _, s := range sensitivity {
		if s == "pii" || s == "critical" {
			return true
		}
	}
	return false
}

// ToolOutcome carries the measured result of a tool execution for
// post-execution policy checks (blast-radius enforcement).
type ToolOutcome struct {
	// RowsAffected is the number of database rows modified.
	// Parse from psql command tag output: "DELETE N", "UPDATE N", "INSERT 0 N".
	RowsAffected int

	// PodsAffected is the number of Kubernetes resources modified.
	// Parse from kubectl output lines ending in " deleted", " configured", etc.
	PodsAffected int

	// Err is the error returned by the tool, if any.
	// When non-nil, post-execution checks are skipped (nothing was executed).
	Err error
}

// CheckResult runs post-execution policy checks with the actual execution context.
// It re-evaluates the policy engine with RowsAffected/PodsAffected populated
// from real tool output and returns an error if a blast-radius condition is violated.
//
// Should be called after every write or destructive tool execution. For read-only
// tools or when Err is set it is a no-op.
func (e *PolicyEnforcer) CheckResult(ctx context.Context, resourceType, resourceName string, action policy.ActionClass, tags []string, outcome ToolOutcome) error {
	if e.engine == nil && e.policyCheckURL == "" {
		return nil
	}
	// Tool itself failed — nothing was executed, blast-radius is irrelevant.
	if outcome.Err != nil {
		return nil
	}
	// Pure reads with no measured impact are always fine.
	if action == policy.ActionRead && outcome.RowsAffected == 0 && outcome.PodsAffected == 0 {
		return nil
	}

	// Remote check mode: delegate blast-radius evaluation to auditd.
	if e.policyCheckURL != "" {
		traceID := ""
		if e.traceStore != nil {
			traceID = e.traceStore.Get()
		}
		principal := audit.PrincipalFromContext(ctx)
		purpose, purposeNote := audit.PurposeFromContext(ctx)
		resp, err := e.callRemotePolicyCheck(ctx, policyCheckReq{
			ResourceType:  resourceType,
			ResourceName:  resourceName,
			Action:        string(action),
			Tags:          tags,
			TraceID:       traceID,
			AgentName:     e.agentName,
			RowsAffected:  outcome.RowsAffected,
			PodsAffected:  outcome.PodsAffected,
			PostExecution: true,
			Principal:     principal,
			Purpose:       purpose,
			PurposeNote:   purposeNote,
		})
		if err != nil {
			return err
		}
		if resp.Effect != "deny" {
			return nil
		}
		slog.Warn("remote post-execution policy check: blast radius exceeded",
			"resource_type", resourceType,
			"resource_name", resourceName,
			"action", action,
			"rows_affected", outcome.RowsAffected,
			"pods_affected", outcome.PodsAffected,
			"policy", resp.PolicyName,
			"message", resp.Message,
		)
		return &policy.DeniedError{
			Decision: policy.Decision{
				Effect:     policy.EffectDeny,
				PolicyName: resp.PolicyName,
				Message:    resp.Message,
			},
			Explanation: resp.Explanation,
		}
	}

	traceID := ""
	if e.traceStore != nil {
		traceID = e.traceStore.Get()
	}
	principal2 := audit.PrincipalFromContext(ctx)
	purpose2, purposeNote2 := audit.PurposeFromContext(ctx)

	req := policy.Request{
		Principal: policy.RequestPrincipal{
			UserID:  principal2.UserID,
			Roles:   principal2.Roles,
			Service: principal2.Service,
		},
		Resource: policy.RequestResource{
			Type: resourceType,
			Name: resourceName,
			Tags: tags,
		},
		Action: action,
		Context: policy.RequestContext{
			TraceID:      traceID,
			RowsAffected: outcome.RowsAffected,
			PodsAffected: outcome.PodsAffected,
			Purpose:      purpose2,
			PurposeNote:  purposeNote2,
		},
	}

	trace := e.engine.Explain(req)
	decision := trace.Decision
	if decision.Effect != policy.EffectDeny {
		return nil
	}

	traceJSON, _ := json.Marshal(trace)

	// Blast-radius violated — audit the post-execution denial and return an error.
	if e.toolAuditor != nil {
		e.toolAuditor.RecordPolicyDecision(ctx, audit.PolicyDecision{
			ResourceType:  resourceType,
			ResourceName:  resourceName,
			Action:        string(action),
			Tags:          tags,
			Effect:        string(decision.Effect),
			PolicyName:    decision.PolicyName,
			RuleIndex:     decision.RuleIndex,
			Message:       decision.Message,
			PostExecution: true,
			Trace:         traceJSON,
			Explanation:   trace.Explanation,
			UserID:        principal2.UserID,
			Roles:         principal2.Roles,
			Service:       principal2.Service,
			AuthMethod:    principal2.AuthMethod,
			Purpose:       purpose2,
			PurposeNote:   purposeNote2,
		})
	}

	slog.Warn("post-execution policy check: blast radius exceeded",
		"resource_type", resourceType,
		"resource_name", resourceName,
		"action", action,
		"rows_affected", outcome.RowsAffected,
		"pods_affected", outcome.PodsAffected,
		"policy", decision.PolicyName,
		"message", decision.Message,
	)

	return &policy.DeniedError{Decision: decision, Explanation: trace.Explanation}
}

// CheckDatabaseResult is a convenience method for post-execution database checks.
func (e *PolicyEnforcer) CheckDatabaseResult(ctx context.Context, dbName string, action policy.ActionClass, tags []string, outcome ToolOutcome) error {
	return e.CheckResult(ctx, "database", dbName, action, tags, outcome)
}

// CheckKubernetesResult is a convenience method for post-execution Kubernetes checks.
func (e *PolicyEnforcer) CheckKubernetesResult(ctx context.Context, namespace string, action policy.ActionClass, tags []string, outcome ToolOutcome) error {
	return e.CheckResult(ctx, "kubernetes", namespace, action, tags, outcome)
}

// CheckDatabaseSessionAge blocks terminate_connection/cancel_query when the
// target session has uncommitted writes in a transaction open longer than the
// max_xact_age_secs policy limit. No-op when hasWrites is false (read-only
// transactions roll back instantly), when xactAgeSecs is 0, or when no policy
// limit is configured.
func (e *PolicyEnforcer) CheckDatabaseSessionAge(ctx context.Context, dbName string, action policy.ActionClass, tags []string, xactAgeSecs int, hasWrites bool) error {
	if e.engine == nil && e.policyCheckURL == "" {
		return nil
	}
	// Read-only transactions roll back instantly; no risk.
	if !hasWrites || xactAgeSecs == 0 {
		return nil
	}

	// Remote check mode.
	if e.policyCheckURL != "" {
		traceID := ""
		if e.traceStore != nil {
			traceID = e.traceStore.Get()
		}
		principal := audit.PrincipalFromContext(ctx)
		purpose, purposeNote := audit.PurposeFromContext(ctx)
		resp, err := e.callRemotePolicyCheck(ctx, policyCheckReq{
			ResourceType: "database",
			ResourceName: dbName,
			Action:       string(action),
			Tags:         tags,
			TraceID:      traceID,
			AgentName:    e.agentName,
			XactAgeSecs:  xactAgeSecs,
			Principal:    principal,
			Purpose:      purpose,
			PurposeNote:  purposeNote,
		})
		if err != nil {
			return err
		}
		if resp.Effect != "deny" {
			return nil
		}
		slog.Warn("remote pre-execution policy check: transaction age exceeded",
			"resource_name", dbName,
			"action", action,
			"xact_age_secs", xactAgeSecs,
			"policy", resp.PolicyName,
			"message", resp.Message,
		)
		return &policy.DeniedError{
			Decision: policy.Decision{
				Effect:     policy.EffectDeny,
				PolicyName: resp.PolicyName,
				Message:    resp.Message,
			},
			Explanation: resp.Explanation,
		}
	}

	traceID := ""
	if e.traceStore != nil {
		traceID = e.traceStore.Get()
	}
	principal3 := audit.PrincipalFromContext(ctx)
	purpose3, purposeNote3 := audit.PurposeFromContext(ctx)

	req := policy.Request{
		Principal: policy.RequestPrincipal{
			UserID:  principal3.UserID,
			Roles:   principal3.Roles,
			Service: principal3.Service,
		},
		Resource: policy.RequestResource{
			Type: "database",
			Name: dbName,
			Tags: tags,
		},
		Action: action,
		Context: policy.RequestContext{
			TraceID:     traceID,
			XactAgeSecs: xactAgeSecs,
			Purpose:     purpose3,
			PurposeNote: purposeNote3,
		},
	}

	trace := e.engine.Explain(req)
	decision := trace.Decision
	if decision.Effect != policy.EffectDeny {
		return nil
	}

	traceJSON, _ := json.Marshal(trace)

	if e.toolAuditor != nil {
		e.toolAuditor.RecordPolicyDecision(ctx, audit.PolicyDecision{
			ResourceType: "database",
			ResourceName: dbName,
			Action:       string(action),
			Tags:         tags,
			Effect:       string(decision.Effect),
			PolicyName:   decision.PolicyName,
			RuleIndex:    decision.RuleIndex,
			Message:      decision.Message,
			Trace:        traceJSON,
			Explanation:  trace.Explanation,
			UserID:       principal3.UserID,
			Roles:        principal3.Roles,
			Service:      principal3.Service,
			AuthMethod:   principal3.AuthMethod,
			Purpose:      purpose3,
			PurposeNote:  purposeNote3,
		})
	}

	slog.Warn("pre-execution policy check: transaction age exceeded",
		"resource_name", dbName,
		"action", action,
		"xact_age_secs", xactAgeSecs,
		"policy", decision.PolicyName,
		"message", decision.Message,
	)
	return &policy.DeniedError{Decision: decision, Explanation: trace.Explanation}
}

// toolNameContextKey is an unexported type to prevent context key collisions.
type toolNameContextKey struct{}

// WithToolName returns a new context carrying the tool name for policy matching.
// Call this at the start of each tool function: ctx = agentutil.WithToolName(ctx, "terminate_connection")
func WithToolName(ctx context.Context, toolName string) context.Context {
	return context.WithValue(ctx, toolNameContextKey{}, toolName)
}

// toolNameFromContext extracts the tool name set by WithToolName, or "" if not set.
func toolNameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(toolNameContextKey{}).(string); ok {
		return v
	}
	return ""
}

// policyCheckReq is the body sent to POST /v1/governance/check.
// Field names match PolicyCheckRequest in cmd/auditd/governance_handlers.go.
type policyCheckReq struct {
	ResourceType  string   `json:"resource_type"`
	ResourceName  string   `json:"resource_name"`
	Action        string   `json:"action"`
	Tags          []string `json:"tags,omitempty"`
	TraceID       string   `json:"trace_id,omitempty"`
	AgentName     string   `json:"agent_name,omitempty"`
	Note          string   `json:"note,omitempty"`
	RowsAffected  int      `json:"rows_affected,omitempty"`
	PodsAffected  int      `json:"pods_affected,omitempty"`
	XactAgeSecs   int      `json:"xact_age_secs,omitempty"`
	PostExecution bool     `json:"post_execution,omitempty"`
	// Identity and purpose propagated from the originating user request.
	Principal   identity.ResolvedPrincipal `json:"principal,omitempty"`
	Purpose     string                     `json:"purpose,omitempty"`
	PurposeNote string                     `json:"purpose_note,omitempty"`
	Sensitivity []string                   `json:"sensitivity,omitempty"`
	ToolName    string                     `json:"tool_name,omitempty"` // specific tool for policy matching
}

// policyCheckResp is the response from POST /v1/governance/check.
// Field names match PolicyCheckResponse in cmd/auditd/governance_handlers.go.
type policyCheckResp struct {
	Effect      string `json:"effect"`
	PolicyName  string `json:"policy_name"`
	Message     string `json:"message"`
	Explanation string `json:"explanation"`
	EventID     string `json:"event_id"`
}

// probeRemotePolicyEngine calls GET /v1/governance/info on the auditd service and
// returns true if the remote has a policy engine configured and enabled.
// The call is best-effort: any network or parse error returns false.
func probeRemotePolicyEngine(checkURL, apiKey string) bool {
	infoURL := strings.TrimRight(checkURL, "/") + "/v1/governance/info"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var info struct {
		Policy *struct {
			Enabled bool `json:"enabled"`
		} `json:"policy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return false
	}
	return info.Policy != nil && info.Policy.Enabled
}

// callRemotePolicyCheck sends a policy check request to the auditd service.
// On any network or server error it returns a non-nil error (fail closed).
func (e *PolicyEnforcer) callRemotePolicyCheck(ctx context.Context, req policyCheckReq) (policyCheckResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return policyCheckResp{}, fmt.Errorf("policy check failed: marshal: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, e.policyCheckTimeout)
	defer cancel()

	checkURL := strings.TrimRight(e.policyCheckURL, "/") + "/v1/governance/check"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, checkURL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("remote policy check: failed to build request; failing closed", "url", checkURL, "err", err)
		return policyCheckResp{}, fmt.Errorf("policy check failed: policy service unreachable")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.policyCheckAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.policyCheckAPIKey)
	}

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		slog.Warn("remote policy check: service unreachable; failing closed", "url", checkURL, "err", err)
		return policyCheckResp{}, fmt.Errorf("policy check failed: policy service unreachable")
	}
	defer httpResp.Body.Close()

	// 403 is the expected status for a deny decision (body still carries the effect).
	// Any other non-2xx (e.g. 503 "policy engine not configured") is an infrastructure
	// error — fail closed rather than silently decoding an error body as an allow.
	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusForbidden {
		slog.Warn("remote policy check: unexpected status; failing closed",
			"url", checkURL, "status", httpResp.StatusCode)
		if httpResp.StatusCode == http.StatusUnauthorized {
			return policyCheckResp{}, fmt.Errorf("policy check failed: agent not authenticated to audit service (set HELPDESK_AUDIT_API_KEY)")
		}
		return policyCheckResp{}, fmt.Errorf("policy check failed: policy service returned %d", httpResp.StatusCode)
	}

	var resp policyCheckResp
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		slog.Warn("remote policy check: invalid response; failing closed",
			"url", checkURL, "status", httpResp.StatusCode, "err", err)
		return policyCheckResp{}, fmt.Errorf("policy check failed: policy service unreachable")
	}
	return resp, nil
}

// handleRemoteResponse converts a policyCheckResp into a Go error.
// For require_approval it invokes the approval workflow when a client is configured.
func (e *PolicyEnforcer) handleRemoteResponse(ctx context.Context, resp policyCheckResp, traceID, resourceType, resourceName string, action policy.ActionClass, tags []string, note string) error {
	switch resp.Effect {
	case "allow":
		return nil
	case "deny":
		return &policy.DeniedError{
			Decision: policy.Decision{
				Effect:     policy.EffectDeny,
				PolicyName: resp.PolicyName,
				Message:    resp.Message,
			},
			Explanation: resp.Explanation,
		}
	case "require_approval":
		if e.approvalClient != nil {
			return e.requestApproval(ctx, traceID, resourceType, resourceName, action, tags, note)
		}
		return &policy.ApprovalRequiredError{
			Decision: policy.Decision{
				Effect:     policy.EffectRequireApproval,
				PolicyName: resp.PolicyName,
				Message:    resp.Message,
			},
		}
	default:
		slog.Warn("remote policy check returned unrecognised effect; treating as allow",
			"effect", resp.Effect, "policy", resp.PolicyName)
		return nil
	}
}

// InitApprovalClient creates an approval client if approvals are enabled.
// Returns nil if approvals are disabled or no audit URL is configured.
// Approvals require a central auditd service.
func InitApprovalClient(cfg Config) *audit.ApprovalClient {
	if !cfg.ApprovalEnabled {
		slog.Debug("approval workflow disabled (HELPDESK_APPROVAL_ENABLED not set)")
		return nil
	}

	if cfg.AuditURL == "" {
		slog.Warn("approval workflow requires HELPDESK_AUDIT_URL to be set")
		return nil
	}

	slog.Info("approval workflow enabled",
		"audit_url", cfg.AuditURL,
		"timeout", cfg.ApprovalTimeout)

	client := audit.NewApprovalClient(cfg.AuditURL)
	if cfg.AuditAPIKey != "" {
		client = client.WithAPIKey(cfg.AuditAPIKey)
	}
	return client
}

// InitAuditStore initializes an audit store for an agent if auditing is enabled.
// Returns nil if auditing is disabled. The caller should defer store.Close() if non-nil.
// If HELPDESK_AUDIT_URL is set, uses the central audit service (preferred).
// Otherwise falls back to local SQLite if HELPDESK_AUDIT_DIR is set.
func InitAuditStore(cfg Config) (audit.Auditor, error) {
	if !cfg.AuditEnabled {
		return nil, nil
	}

	// Prefer central audit service
	if cfg.AuditURL != "" {
		slog.Info("agent audit logging enabled (remote)", "url", cfg.AuditURL)
		store := audit.NewRemoteStore(cfg.AuditURL)
		if cfg.AuditAPIKey != "" {
			store = store.WithAPIKey(cfg.AuditAPIKey)
		}
		return store, nil
	}

	// Fall back to local SQLite
	auditDir := cfg.AuditDir
	if auditDir == "" {
		auditDir = "."
	}

	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(auditDir, "audit.db"),
		// No socket for agents - only the orchestrator needs the socket
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create audit store: %w", err)
	}

	slog.Info("agent audit logging enabled (local)", "db", filepath.Join(auditDir, "audit.db"))
	return store, nil
}

// CardOptions allows agents to customize the AgentCard beyond the defaults
// that Serve derives automatically from the ADK agent.
type CardOptions struct {
	// Version is the agent's version string (e.g., "1.0.0").
	Version string

	// DocumentationURL points to the agent's documentation.
	DocumentationURL string

	// Provider describes the organization providing this agent.
	Provider *a2a.AgentProvider

	// SkillTags maps a skill ID to additional tags to merge onto the
	// auto-generated skills. Skill IDs follow the ADK pattern:
	// "agentName" for the model skill, "agentName-toolName" for tool skills.
	SkillTags map[string][]string

	// SkillExamples maps a skill ID to example prompts/scenarios.
	SkillExamples map[string][]string

	// SkillAutoRemediationEligible declares which skills may execute without
	// per-step approval in agent_auto playbooks (subject to customer policy
	// and the playbook's permitted_tools list).
	SkillAutoRemediationEligible map[string]bool

	// SkillFleetEligible declares which skills are eligible for fleet jobs.
	// Only fleet-eligible tools are shown to the fleet planner.
	// Skill IDs that are absent or false are invisible to the planner.
	SkillFleetEligible map[string]bool

	// SkillCapabilities maps a skill ID to capability constants (see toolregistry.Cap*).
	// Capabilities describe what data a tool provides in a closed vocabulary.
	SkillCapabilities map[string][]string

	// SkillSupersedes maps a skill ID to the tool names it makes redundant.
	// When a superseding tool is selected, the planner removes the superseded ones.
	SkillSupersedes map[string][]string

	// SkillSchemaHash maps a skill ID to its schema fingerprint.
	// Computed by ComputeSchemaFingerprints; serialized as "schema_hash:<fingerprint>"
	// tags so the toolregistry can read it back during discovery.
	SkillSchemaHash map[string]string

	// ToolSchemas maps a tool name (without agent prefix) to its JSON Schema properties.
	// Computed by ComputeInputSchemas; served at GET /schemas for gateway discovery.
	ToolSchemas map[string]map[string]any
}

// applyCardOptions merges optional metadata onto an AgentCard.
func applyCardOptions(card *a2a.AgentCard, opts CardOptions) {
	if opts.Version != "" {
		card.Version = opts.Version
	}
	if opts.DocumentationURL != "" {
		card.DocumentationURL = opts.DocumentationURL
	}
	if opts.Provider != nil {
		card.Provider = opts.Provider
	}
	for i := range card.Skills {
		skill := &card.Skills[i]
		if tags, ok := opts.SkillTags[skill.ID]; ok {
			skill.Tags = append(skill.Tags, tags...)
		}
		if examples, ok := opts.SkillExamples[skill.ID]; ok {
			skill.Examples = examples
		}
		// Serialize typed taxonomy fields as key:value tag strings.
		// This keeps the A2A card transport unchanged while providing
		// compile-time type safety to agent authors.
		if v, ok := opts.SkillFleetEligible[skill.ID]; ok && v {
			skill.Tags = append(skill.Tags, "fleet:true")
		}
		if v, ok := opts.SkillAutoRemediationEligible[skill.ID]; ok && v {
			skill.Tags = append(skill.Tags, "auto_remediation:true")
		}
		for _, cap := range opts.SkillCapabilities[skill.ID] {
			skill.Tags = append(skill.Tags, "cap:"+cap)
		}
		for _, sup := range opts.SkillSupersedes[skill.ID] {
			skill.Tags = append(skill.Tags, "supersedes:"+sup)
		}
		if hash, ok := opts.SkillSchemaHash[skill.ID]; ok && hash != "" {
			skill.Tags = append(skill.Tags, "schema_hash:"+hash)
		}
	}
}

// registerSchemasHandler registers GET /schemas on mux unconditionally.
// The endpoint returns a JSON object mapping tool name → JSON Schema properties,
// allowing gateway discovery to populate ToolEntry.InputSchema.
// When schemas is nil or empty, it returns an empty JSON object so that
// agents without declared schemas do not cause gateway discovery to skip them.
func registerSchemasHandler(mux *http.ServeMux, schemas map[string]map[string]any) {
	if schemas == nil {
		schemas = map[string]map[string]any{}
	}
	b, err := json.Marshal(schemas)
	if err != nil {
		slog.Warn("agentutil: failed to marshal tool schemas", "err", err)
		return
	}
	mux.HandleFunc("GET /schemas", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b) //nolint:errcheck
	})
}

// declarationProvider matches tools that expose Declaration().
// Mirrors the unexported interface in internal/model/anthropic.go.
type declarationProvider interface {
	Declaration() *genai.FunctionDeclaration
}

// toolSchemaSource returns the schema source for a tool declaration.
// functiontool.New populates ParametersJsonSchema (type any); older/custom tools
// may use Parameters (*genai.Schema). We prefer ParametersJsonSchema when set.
func toolSchemaSource(decl *genai.FunctionDeclaration) any {
	if decl.ParametersJsonSchema != nil {
		return decl.ParametersJsonSchema
	}
	if decl.Parameters != nil {
		return decl.Parameters
	}
	return nil
}

// ComputeSchemaFingerprints computes a schema fingerprint (first 12 hex chars of
// sha256 of the tool's parameter schema JSON) for each tool in the slice.
// Returns a map of skillID → fingerprint suitable for CardOptions.SkillSchemaHash.
// skillID format: "<agentName>-<toolName>" (matches the CardOptions key convention).
// Tools without a Declaration or with no parameter schema are omitted.
func ComputeSchemaFingerprints(agentName string, tools []tool.Tool) map[string]string {
	result := make(map[string]string, len(tools))
	for _, t := range tools {
		dp, ok := t.(declarationProvider)
		if !ok {
			continue
		}
		decl := dp.Declaration()
		if decl == nil {
			continue
		}
		src := toolSchemaSource(decl)
		if src == nil {
			continue
		}
		b, err := json.Marshal(src)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		fingerprint := fmt.Sprintf("%x", sum[:6]) // 12 hex chars
		skillID := agentName + "-" + t.Name()
		result[skillID] = fingerprint
	}
	return result
}

// ComputeInputSchemas returns a map of toolName → JSON Schema (map[string]any) for use
// in CardOptions.ToolSchemas. Served at GET /schemas so gateway discovery can populate
// ToolEntry.InputSchema.
// Tools without a Declaration or with no parameter schema are omitted.
func ComputeInputSchemas(tools []tool.Tool) map[string]map[string]any {
	result := make(map[string]map[string]any, len(tools))
	for _, t := range tools {
		dp, ok := t.(declarationProvider)
		if !ok {
			continue
		}
		decl := dp.Declaration()
		if decl == nil {
			continue
		}
		src := toolSchemaSource(decl)
		if src == nil {
			continue
		}
		b, err := json.Marshal(src)
		if err != nil {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(b, &schema); err != nil {
			continue
		}
		result[t.Name()] = schema
	}
	return result
}

// Serve starts an A2A server for the given agent on cfg.ListenAddr.
// It sets up the agent card, JSON-RPC handler, in-memory session service, and blocks.
// An optional CardOptions can be passed to enrich the agent card with additional metadata.
// If cfg.ExternalURL is set, it will be used in the agent card instead of the listener address.
func Serve(ctx context.Context, a agent.Agent, cfg Config, opts ...CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	// Use ExternalURL if set, otherwise fall back to listener address.
	// ExternalURL is required in Kubernetes where pods communicate via service names.
	var baseURL *url.URL
	if cfg.ExternalURL != "" {
		baseURL, err = url.Parse(cfg.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid HELPDESK_AGENT_URL: %v", err)
		}
	} else {
		baseURL = &url.URL{Scheme: "http", Host: listener.Addr().String()}
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               a.Name(),
		Description:        a.Description(),
		Skills:             adka2a.BuildAgentSkills(a),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	if len(opts) > 0 {
		applyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	var toolSchemas map[string]map[string]any
	if len(opts) > 0 {
		toolSchemas = opts[0].ToolSchemas
	}
	registerSchemasHandler(mux, toolSchemas)

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
	})
	requestHandler := a2asrv.NewHandler(executor)
	mux.Handle(agentPath, a2asrv.NewJSONRPCHandler(requestHandler))

	slog.Info("starting A2A server",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}

// NewReasoningCallback returns an ADK AfterModelCallback that captures agent-level
// LLM reasoning to the audit trail. It emits an agent_reasoning event whenever
// the model produces both text (deliberation) and function calls (tool decision)
// in the same response — bridging the gap between policy-decision events and
// tool-execution events with the model's own rationale.
//
// Returning nil, nil from an AfterModelCallback leaves the original response unchanged.
func NewReasoningCallback(auditor *audit.ToolAuditor) func(agent.CallbackContext, *adkmodel.LLMResponse, error) (*adkmodel.LLMResponse, error) {
	return func(ctx agent.CallbackContext, llmResponse *adkmodel.LLMResponse, llmResponseError error) (*adkmodel.LLMResponse, error) {
		if auditor == nil || llmResponse == nil || llmResponse.Content == nil {
			return nil, nil
		}
		var textParts []string
		var toolCalls []string
		for _, part := range llmResponse.Content.Parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			if part.FunctionCall != nil {
				toolCalls = append(toolCalls, part.FunctionCall.Name)
			}
		}
		// Only audit when the model deliberates (text) AND decides on a tool (function call).
		// Pure-text responses are final answers; pure function calls have no reasoning to capture.
		if len(textParts) > 0 && len(toolCalls) > 0 {
			auditor.RecordAgentReasoning(ctx, strings.Join(textParts, "\n\n"), toolCalls)
		}
		return nil, nil
	}
}

// ServeWithTracing starts an A2A server with trace_id extraction from incoming messages.
// The traceStore is populated with trace_id from A2A message metadata for each request.
// When auditor is non-nil, a gateway_request anchor event is emitted for every incoming
// NL-query request so the request is visible as a journey even without an upstream gateway.
func ServeWithTracing(ctx context.Context, a agent.Agent, cfg Config, traceStore *audit.CurrentTraceStore, auditor audit.Auditor, opts ...CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	var baseURL *url.URL
	if cfg.ExternalURL != "" {
		baseURL, err = url.Parse(cfg.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid HELPDESK_AGENT_URL: %v", err)
		}
	} else {
		baseURL = &url.URL{Scheme: "http", Host: listener.Addr().String()}
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               a.Name(),
		Description:        a.Description(),
		Skills:             adka2a.BuildAgentSkills(a),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	if len(opts) > 0 {
		applyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	var toolSchemas map[string]map[string]any
	if len(opts) > 0 {
		toolSchemas = opts[0].ToolSchemas
	}
	registerSchemasHandler(mux, toolSchemas)

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
	})
	requestHandler := a2asrv.NewHandler(executor)

	// Wrap with audit-aware trace middleware: extracts or generates a trace_id for
	// every request and emits a gateway_request anchor event so direct agent calls
	// are visible as journeys even when bypassing the orchestrator/gateway.
	tracedHandler := audit.TraceMiddlewareWithAudit(traceStore, auditor, a.Name(), a2asrv.NewJSONRPCHandler(requestHandler))
	mux.Handle(agentPath, tracedHandler)

	slog.Info("starting A2A server with tracing",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}

// ServeWithTracingAndDirectTools is like ServeWithTracing but also registers
// a POST /tool/{name} endpoint for deterministic, LLM-bypassing tool dispatch.
// The registry maps tool names to context.Context-based handler functions.
// Fleet runner uses this path to execute structured tool calls without LLM narration.
func ServeWithTracingAndDirectTools(ctx context.Context, a agent.Agent, cfg Config, traceStore *audit.CurrentTraceStore, auditor audit.Auditor, registry *DirectToolRegistry, opts ...CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	var baseURL *url.URL
	if cfg.ExternalURL != "" {
		baseURL, err = url.Parse(cfg.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid HELPDESK_AGENT_URL: %v", err)
		}
	} else {
		baseURL = &url.URL{Scheme: "http", Host: listener.Addr().String()}
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               a.Name(),
		Description:        a.Description(),
		Skills:             adka2a.BuildAgentSkills(a),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	if len(opts) > 0 {
		applyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	var toolSchemas map[string]map[string]any
	if len(opts) > 0 {
		toolSchemas = opts[0].ToolSchemas
	}
	registerSchemasHandler(mux, toolSchemas)

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
	})
	requestHandler := a2asrv.NewHandler(executor)

	tracedHandler := audit.TraceMiddlewareWithAudit(traceStore, auditor, a.Name(), a2asrv.NewJSONRPCHandler(requestHandler))
	mux.Handle(agentPath, tracedHandler)

	if registry != nil {
		// Build inbound auth for POST /tool/{name}.
		// When HELPDESK_USERS_FILE is set, only service accounts with a valid
		// Bearer API key are permitted. Otherwise logs a warning and leaves the
		// endpoint open (dev/local mode, same behaviour as auditd with no users file).
		var idProvider identity.Provider = &identity.NoAuthProvider{}
		enforcing := cfg.UsersFile != ""
		if cfg.UsersFile != "" {
			p, err := identity.NewStaticProvider(cfg.UsersFile)
			if err != nil {
				return fmt.Errorf("agent inbound auth: failed to load users file %q: %w", cfg.UsersFile, err)
			}
			idProvider = p
			slog.Info("agent inbound auth enabled", "users_file", cfg.UsersFile)
		} else {
			slog.Warn("POST /tool/{name} is unauthenticated — set HELPDESK_USERS_FILE to require service-account credentials")
		}
		authzr := authz.NewAuthorizer(authz.DefaultAgentPermissions, enforcing)
		registerDirectToolRoutes(mux, registry, traceStore, idProvider, authzr)
		slog.Info("direct tool dispatch enabled", "agent", a.Name(), "tools", len(registry.tools))
	}

	slog.Info("starting A2A server with tracing",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}

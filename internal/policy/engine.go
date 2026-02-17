package policy

import (
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

// Engine evaluates policy decisions for requests.
type Engine struct {
	config        *Config
	defaultEffect Effect
	dryRun        bool
}

// EngineConfig configures the policy engine.
type EngineConfig struct {
	// PolicyConfig is the loaded policy configuration.
	PolicyConfig *Config

	// DefaultEffect is the effect when no policy matches.
	// Defaults to EffectDeny.
	DefaultEffect Effect

	// DryRun logs decisions but always returns allow.
	DryRun bool
}

// NewEngine creates a new policy engine.
func NewEngine(cfg EngineConfig) *Engine {
	if cfg.PolicyConfig == nil {
		cfg.PolicyConfig = DefaultConfig()
	}
	if cfg.DefaultEffect == "" {
		cfg.DefaultEffect = EffectDeny
	}

	return &Engine{
		config:        cfg.PolicyConfig,
		defaultEffect: cfg.DefaultEffect,
		dryRun:        cfg.DryRun,
	}
}

// Evaluate evaluates a request against all policies and returns a decision.
func (e *Engine) Evaluate(req Request) Decision {
	// Set default timestamp if not provided
	if req.Context.Timestamp.IsZero() {
		req.Context.Timestamp = time.Now()
	}

	decision := e.evaluate(req)

	// Log the decision
	logDecision(req, decision, e.dryRun)

	// In dry-run mode, always allow but preserve the decision info
	if e.dryRun && decision.Effect != EffectAllow {
		decision.Message = "[DRY RUN] " + decision.Message
		decision.Effect = EffectAllow
	}

	return decision
}

func (e *Engine) evaluate(req Request) Decision {
	// Find matching policies
	for _, policy := range e.config.Policies {
		if !policy.IsEnabled() {
			continue
		}

		// Check if policy applies to this principal
		if !e.matchesPrincipal(policy, req.Principal) {
			continue
		}

		// Check if policy applies to this resource
		if !e.matchesResource(policy, req.Resource) {
			continue
		}

		// Evaluate rules in order
		for i, rule := range policy.Rules {
			if !rule.Action.Matches(req.Action) {
				continue
			}

			// Check schedule condition
			if rule.Conditions != nil && rule.Conditions.Schedule != nil {
				if !rule.Conditions.Schedule.IsActive(req.Context.Timestamp) {
					continue // Schedule doesn't match, try next rule
				}
			}

			// Found a matching rule
			decision := Decision{
				Effect:     rule.Effect,
				PolicyName: policy.Name,
				RuleIndex:  i,
				Message:    rule.Message,
			}

			// Apply conditions
			if rule.Conditions != nil {
				decision = e.applyConditions(decision, rule.Conditions, req)
			}

			return decision
		}
	}

	// No matching policy/rule - use default
	return Decision{
		Effect:     e.defaultEffect,
		PolicyName: "default",
		Message:    "No matching policy found",
	}
}

// matchesPrincipal checks if a policy applies to the given principal.
func (e *Engine) matchesPrincipal(policy Policy, principal RequestPrincipal) bool {
	// If no principals specified, policy applies to everyone
	if len(policy.Principals) == 0 {
		return true
	}

	for _, p := range policy.Principals {
		if p.Any {
			return true
		}
		if p.User != "" && p.User == principal.UserID {
			return true
		}
		if p.Service != "" && p.Service == principal.Service {
			return true
		}
		if p.Role != "" {
			for _, role := range principal.Roles {
				if role == p.Role {
					return true
				}
			}
		}
	}

	return false
}

// matchesResource checks if a policy applies to the given resource.
func (e *Engine) matchesResource(policy Policy, resource RequestResource) bool {
	for _, r := range policy.Resources {
		if r.Type != resource.Type {
			continue
		}

		// Check match criteria
		if r.Match.Name != "" && r.Match.Name != resource.Name {
			continue
		}

		if r.Match.NamePattern != "" {
			matched, _ := filepath.Match(r.Match.NamePattern, resource.Name)
			if !matched {
				continue
			}
		}

		if r.Match.Namespace != "" && r.Match.Namespace != resource.Namespace {
			continue
		}

		if len(r.Match.Tags) > 0 {
			if !hasAllTags(resource.Tags, r.Match.Tags) {
				continue
			}
		}

		// All criteria matched
		return true
	}

	return false
}

// hasAllTags returns true if resourceTags contains all requiredTags.
func hasAllTags(resourceTags, requiredTags []string) bool {
	tagSet := make(map[string]bool)
	for _, t := range resourceTags {
		tagSet[t] = true
	}
	for _, t := range requiredTags {
		if !tagSet[t] {
			return false
		}
	}
	return true
}

// applyConditions applies rule conditions to the decision.
func (e *Engine) applyConditions(decision Decision, cond *Conditions, req Request) Decision {
	// Check approval requirement
	if cond.RequireApproval {
		decision.RequiresApproval = true
		decision.ApprovalQuorum = cond.ApprovalQuorum
		if decision.ApprovalQuorum == 0 {
			decision.ApprovalQuorum = 1
		}
		if decision.Effect == EffectAllow {
			decision.Effect = EffectRequireApproval
		}
	}

	// Check blast radius limits
	if cond.MaxRowsAffected > 0 && req.Context.RowsAffected > cond.MaxRowsAffected {
		decision.Effect = EffectDeny
		decision.Message = formatMessage("Operation affects %d rows, limit is %d",
			req.Context.RowsAffected, cond.MaxRowsAffected)
		decision.Conditions = append(decision.Conditions,
			formatMessage("max %d rows", cond.MaxRowsAffected))
	}

	if cond.MaxPodsAffected > 0 && req.Context.PodsAffected > cond.MaxPodsAffected {
		decision.Effect = EffectDeny
		decision.Message = formatMessage("Operation affects %d pods, limit is %d",
			req.Context.PodsAffected, cond.MaxPodsAffected)
		decision.Conditions = append(decision.Conditions,
			formatMessage("max %d pods", cond.MaxPodsAffected))
	}

	return decision
}

func formatMessage(format string, args ...any) string {
	return strings.TrimSpace(strings.ReplaceAll(
		strings.ReplaceAll(format, "%d", "%v"),
		"%s", "%v",
	))
}

func logDecision(req Request, decision Decision, dryRun bool) {
	attrs := []any{
		"action", req.Action,
		"resource_type", req.Resource.Type,
		"resource_name", req.Resource.Name,
		"effect", decision.Effect,
		"policy", decision.PolicyName,
	}

	if req.Principal.UserID != "" {
		attrs = append(attrs, "user", req.Principal.UserID)
	}
	if req.Principal.Service != "" {
		attrs = append(attrs, "service", req.Principal.Service)
	}
	if decision.Message != "" {
		attrs = append(attrs, "message", decision.Message)
	}
	if dryRun {
		attrs = append(attrs, "dry_run", true)
	}

	switch decision.Effect {
	case EffectDeny:
		slog.Warn("policy decision: DENY", attrs...)
	case EffectRequireApproval:
		slog.Info("policy decision: REQUIRE_APPROVAL", attrs...)
	default:
		slog.Debug("policy decision: ALLOW", attrs...)
	}
}

// MustAllow is a convenience method that returns an error if the decision is not allow.
func (d *Decision) MustAllow() error {
	if d.IsAllowed() {
		return nil
	}
	if d.NeedsApproval() {
		return &ApprovalRequiredError{Decision: *d}
	}
	return &DeniedError{Decision: *d}
}

// DeniedError is returned when a request is denied by policy.
type DeniedError struct {
	Decision Decision
}

func (e *DeniedError) Error() string {
	if e.Decision.Message != "" {
		return "policy denied: " + e.Decision.Message
	}
	return "policy denied by " + e.Decision.PolicyName
}

// ApprovalRequiredError is returned when a request requires approval.
type ApprovalRequiredError struct {
	Decision Decision
}

func (e *ApprovalRequiredError) Error() string {
	if e.Decision.Message != "" {
		return "approval required: " + e.Decision.Message
	}
	return "approval required by policy " + e.Decision.PolicyName
}

// IsApprovalRequired returns true if the error indicates approval is required.
func IsApprovalRequired(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*ApprovalRequiredError)
	return ok
}

// IsDenied returns true if the error indicates the request was denied.
func IsDenied(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*DeniedError)
	return ok
}

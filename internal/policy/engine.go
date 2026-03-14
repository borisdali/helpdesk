package policy

import (
	"fmt"
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

// Config returns the policy configuration.
func (e *Engine) Config() *Config {
	return e.config
}

// Evaluate evaluates a request against all policies and returns a decision.
// It is a thin wrapper around Explain that discards the trace.
func (e *Engine) Evaluate(req Request) Decision {
	return e.Explain(req).Decision
}

// Explain evaluates a request and returns both the decision and a full
// evaluation trace suitable for human-readable explanations and audit storage.
func (e *Engine) Explain(req Request) DecisionTrace {
	if req.Context.Timestamp.IsZero() {
		req.Context.Timestamp = time.Now()
	}

	trace := e.explainEvaluate(req)
	trace.Explanation = buildExplanation(req, trace)

	logDecision(req, trace.Decision, e.dryRun)

	if e.dryRun && trace.Decision.Effect != EffectAllow {
		trace.Decision.Message = "[DRY RUN] " + trace.Decision.Message
		trace.Decision.Effect = EffectAllow
	}

	return trace
}

// explainEvaluate performs the full policy evaluation while building a trace.
func (e *Engine) explainEvaluate(req Request) DecisionTrace {
	var trace DecisionTrace

	for _, pol := range e.config.Policies {
		pt := PolicyTrace{PolicyName: pol.Name}

		if !pol.IsEnabled() {
			pt.SkipReason = "disabled"
			trace.PoliciesEvaluated = append(trace.PoliciesEvaluated, pt)
			continue
		}
		if !e.matchesPrincipal(pol, req.Principal) {
			pt.SkipReason = "principal_mismatch"
			trace.PoliciesEvaluated = append(trace.PoliciesEvaluated, pt)
			continue
		}
		if !e.matchesResource(pol, req.Resource) {
			pt.SkipReason = "resource_mismatch"
			pt.RequiredTags = resourceTagSets(pol, req.Resource.Type)
			trace.PoliciesEvaluated = append(trace.PoliciesEvaluated, pt)
			continue
		}

		pt.Matched = true

		for i, rule := range pol.Rules {
			rt := RuleTrace{
				Index:   i,
				Actions: actionsToStrings(rule.Action),
				Effect:  string(rule.Effect),
			}

			if !rule.Action.Matches(req.Action) {
				rt.SkipReason = "action_mismatch"
				pt.Rules = append(pt.Rules, rt)
				continue
			}
			if rule.Conditions != nil && rule.Conditions.Schedule != nil {
				if !rule.Conditions.Schedule.IsActive(req.Context.Timestamp) {
					rt.SkipReason = "schedule_inactive"
					pt.Rules = append(pt.Rules, rt)
					continue
				}
			}

			// This rule matched.
			rt.Matched = true
			decision := Decision{
				Effect:     rule.Effect,
				PolicyName: pol.Name,
				RuleIndex:  i,
				Message:    rule.Message,
			}
			if rule.Conditions != nil {
				decision, rt.Conditions = e.applyConditionsWithTrace(decision, rule.Conditions, req)
			}
			rt.Effect = string(decision.Effect) // may have changed (e.g. blast radius → deny)

			pt.Rules = append(pt.Rules, rt)
			trace.PoliciesEvaluated = append(trace.PoliciesEvaluated, pt)
			trace.Decision = decision
			return trace
		}

		trace.PoliciesEvaluated = append(trace.PoliciesEvaluated, pt)
	}

	// No matching policy/rule — apply default.
	trace.DefaultApplied = true
	trace.Decision = Decision{
		Effect:     e.defaultEffect,
		PolicyName: "default",
		Message:    "No matching policy found",
	}
	return trace
}

func actionsToStrings(am ActionMatcher) []string {
	s := make([]string, len(am))
	for i, a := range am {
		s[i] = string(a)
	}
	return s
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

		// Sensitivity matching: all listed classes must be present on the resource.
		if len(r.Match.Sensitivity) > 0 {
			if !allPresent(r.Match.Sensitivity, resource.Sensitivity) {
				continue
			}
		}

		// All criteria matched
		return true
	}

	return false
}

// allPresent returns true if every item in required is present in available.
func allPresent(required, available []string) bool {
	avail := make(map[string]bool, len(available))
	for _, s := range available {
		avail[s] = true
	}
	for _, s := range required {
		if !avail[s] {
			return false
		}
	}
	return true
}

// resourceTagSets returns the tag sets from a policy's resource specs that
// match the given resource type and have tag requirements. Used to populate
// PolicyTrace.RequiredTags so callers can explain which tags would unlock a policy.
func resourceTagSets(pol Policy, resourceType string) [][]string {
	var sets [][]string
	for _, r := range pol.Resources {
		if r.Type == resourceType && len(r.Match.Tags) > 0 {
			cp := make([]string, len(r.Match.Tags))
			copy(cp, r.Match.Tags)
			sets = append(sets, cp)
		}
	}
	return sets
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

// applyConditionsWithTrace applies rule conditions to a decision and returns
// a ConditionTrace for each condition checked.
func (e *Engine) applyConditionsWithTrace(decision Decision, cond *Conditions, req Request) (Decision, []ConditionTrace) {
	var traces []ConditionTrace

	if cond.RequireApproval {
		decision.RequiresApproval = true
		decision.ApprovalQuorum = cond.ApprovalQuorum
		if decision.ApprovalQuorum == 0 {
			decision.ApprovalQuorum = 1
		}
		if decision.Effect == EffectAllow {
			decision.Effect = EffectRequireApproval
		}
		traces = append(traces, ConditionTrace{
			Name:   "require_approval",
			Passed: true,
			Detail: fmt.Sprintf("quorum: %d", decision.ApprovalQuorum),
		})
	}

	if cond.MaxRowsAffected > 0 {
		exceeded := req.Context.RowsAffected > cond.MaxRowsAffected
		ct := ConditionTrace{
			Name:   "max_rows_affected",
			Passed: !exceeded,
			Detail: fmt.Sprintf("%d rows affected, limit is %d", req.Context.RowsAffected, cond.MaxRowsAffected),
		}
		if exceeded {
			decision.Effect = EffectDeny
			decision.Message = formatMessage("Operation affects %d rows, limit is %d",
				req.Context.RowsAffected, cond.MaxRowsAffected)
			decision.Conditions = append(decision.Conditions,
				formatMessage("max %d rows", cond.MaxRowsAffected))
		}
		traces = append(traces, ct)
	}

	if cond.MaxPodsAffected > 0 {
		exceeded := req.Context.PodsAffected > cond.MaxPodsAffected
		ct := ConditionTrace{
			Name:   "max_pods_affected",
			Passed: !exceeded,
			Detail: fmt.Sprintf("%d pods affected, limit is %d", req.Context.PodsAffected, cond.MaxPodsAffected),
		}
		if exceeded {
			decision.Effect = EffectDeny
			decision.Message = formatMessage("Operation affects %d pods, limit is %d",
				req.Context.PodsAffected, cond.MaxPodsAffected)
			decision.Conditions = append(decision.Conditions,
				formatMessage("max %d pods", cond.MaxPodsAffected))
		}
		traces = append(traces, ct)
	}

	if cond.MaxXactAgeSecs > 0 && req.Context.XactAgeSecs > 0 {
		exceeded := req.Context.XactAgeSecs > cond.MaxXactAgeSecs
		ct := ConditionTrace{
			Name:   "max_xact_age_secs",
			Passed: !exceeded,
			Detail: fmt.Sprintf("transaction age %ds, limit %ds", req.Context.XactAgeSecs, cond.MaxXactAgeSecs),
		}
		if exceeded {
			decision.Effect = EffectDeny
			decision.Message = formatMessage(
				"Transaction has been open for %s; rollback may take as long. Limit is %s",
				fmtAgeSecs(req.Context.XactAgeSecs), fmtAgeSecs(cond.MaxXactAgeSecs))
			decision.Conditions = append(decision.Conditions,
				formatMessage("max_xact_age: %ds", cond.MaxXactAgeSecs))
		}
		traces = append(traces, ct)
	}

	// Purpose: AllowedPurposes
	if len(cond.AllowedPurposes) > 0 {
		purpose := req.Context.Purpose
		allowed := false
		for _, p := range cond.AllowedPurposes {
			if p == purpose {
				allowed = true
				break
			}
		}
		ct := ConditionTrace{
			Name:   "allowed_purposes",
			Passed: allowed,
			Detail: fmt.Sprintf("purpose=%q allowed=%v", purpose, cond.AllowedPurposes),
		}
		traces = append(traces, ct)
		if !allowed {
			decision.Effect = EffectDeny
			decision.Message = formatMessage("Purpose %q is not in the allowed list %v", purpose, cond.AllowedPurposes)
			decision.Conditions = append(decision.Conditions,
				formatMessage("allowed_purposes: %v", cond.AllowedPurposes))
			return decision, traces
		}
	}

	// Purpose: BlockedPurposes
	if len(cond.BlockedPurposes) > 0 {
		purpose := req.Context.Purpose
		blocked := false
		for _, p := range cond.BlockedPurposes {
			if p == purpose {
				blocked = true
				break
			}
		}
		ct := ConditionTrace{
			Name:   "blocked_purposes",
			Passed: !blocked,
			Detail: fmt.Sprintf("purpose=%q blocked=%v", purpose, cond.BlockedPurposes),
		}
		traces = append(traces, ct)
		if blocked {
			decision.Effect = EffectDeny
			decision.Message = formatMessage("Purpose %q is in the blocked list %v", purpose, cond.BlockedPurposes)
			decision.Conditions = append(decision.Conditions,
				formatMessage("blocked_purposes: %v", cond.BlockedPurposes))
			return decision, traces
		}
	}

	return decision, traces
}

// fmtAgeSecs formats a duration in seconds as "Xh Ym" or "Xs" for policy messages.
func fmtAgeSecs(secs int) string {
	if secs >= 3600 {
		return fmt.Sprintf("%dh %dm", secs/3600, (secs%3600)/60)
	}
	if secs >= 60 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%ds", secs)
}

func formatMessage(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
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
	if len(req.Resource.Sensitivity) > 0 {
		attrs = append(attrs, "resource_sensitivity", req.Resource.Sensitivity)
	}
	if req.Context.Purpose != "" {
		attrs = append(attrs, "purpose", req.Context.Purpose)
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
	Decision    Decision
	Explanation string // human-readable explanation from DecisionTrace; overrides the terse default
}

func (e *DeniedError) Error() string {
	if e.Explanation != "" {
		return e.Explanation
	}
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

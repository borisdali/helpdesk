package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/internal/policy"
)

// governanceServer handles governance-related HTTP endpoints.
type governanceServer struct {
	auditStore    *audit.Store
	approvalStore *audit.ApprovalStore
	notifier      *ApprovalNotifier
	policyEngine  *policy.Engine
	policyFile    string
	infraConfig   *infra.Config // loaded from HELPDESK_INFRA_CONFIG for tag resolution
}

// GovernanceInfo is the response for GET /v1/governance/info.
type GovernanceInfo struct {
	// Policy information
	Policy *PolicyInfo `json:"policy,omitempty"`

	// Approval workflow configuration
	Approvals ApprovalConfig `json:"approvals"`

	// Audit system status
	Audit AuditStatus `json:"audit"`

	// Timestamp of this info
	Timestamp string `json:"timestamp"`
}

// PolicyInfo describes the loaded policy configuration.
type PolicyInfo struct {
	Enabled       bool            `json:"enabled"`
	File          string          `json:"file,omitempty"`
	PoliciesCount int             `json:"policies_count"`
	RulesCount    int             `json:"rules_count"`
	Policies      []PolicySummary `json:"policies,omitempty"`
}

// PolicySummary is a summary of a policy and its rules.
type PolicySummary struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Enabled     bool          `json:"enabled"`
	Resources   []string      `json:"resources,omitempty"`
	Rules       []RuleSummary `json:"rules"`
}

// RuleSummary is a summary of a policy rule.
type RuleSummary struct {
	Actions    []string `json:"actions"`
	Effect     string   `json:"effect"`
	Message    string   `json:"message,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
}

// ApprovalConfig describes the approval workflow configuration.
type ApprovalConfig struct {
	Enabled          bool   `json:"enabled"`
	WebhookConfigured bool   `json:"webhook_configured"`
	EmailConfigured   bool   `json:"email_configured"`
	DefaultTimeout   string `json:"default_timeout"`
	PendingCount     int    `json:"pending_count"`
}

// AuditStatus describes the audit system status.
type AuditStatus struct {
	Enabled     bool   `json:"enabled"`
	EventsTotal int    `json:"events_total"`
	ChainValid  bool   `json:"chain_valid"`
	LastEventAt string `json:"last_event_at,omitempty"`
}

// newGovernanceServer creates a governance server with optional policy loading.
func newGovernanceServer(
	auditStore *audit.Store,
	approvalStore *audit.ApprovalStore,
	notifier *ApprovalNotifier,
) *governanceServer {
	gs := &governanceServer{
		auditStore:    auditStore,
		approvalStore: approvalStore,
		notifier:      notifier,
	}

	// Load policy file when explicitly enabled.
	// Backward compat: also load when HELPDESK_POLICY_FILE is set without the flag.
	policyEnabled := os.Getenv("HELPDESK_POLICY_ENABLED")
	policyFile := os.Getenv("HELPDESK_POLICY_FILE")
	shouldLoad := policyEnabled == "true" || policyEnabled == "1" ||
		(policyEnabled == "" && policyFile != "")
	if shouldLoad && policyFile != "" {
		gs.policyFile = policyFile
		cfg, err := policy.LoadFile(policyFile)
		if err == nil {
			gs.policyEngine = policy.NewEngine(policy.EngineConfig{
				PolicyConfig: cfg,
			})
			slog.Info("policy engine loaded",
				"file", policyFile,
				"policies", len(cfg.Policies))
		} else {
			slog.Warn("failed to load policy file for governance info", "file", policyFile, "err", err)
		}
	} else {
		slog.Info("policy engine disabled (governance/check returns 503)",
			"HELPDESK_POLICY_ENABLED", policyEnabled,
			"HELPDESK_POLICY_FILE", policyFile)
	}

	// Load infrastructure config so the explain endpoint can resolve tags by resource name.
	if infraPath := os.Getenv("HELPDESK_INFRA_CONFIG"); infraPath != "" {
		ic, err := infra.Load(infraPath)
		if err == nil {
			gs.infraConfig = ic
			slog.Info("infra config loaded for tag resolution",
				"databases", len(ic.DBServers),
				"k8s_clusters", len(ic.K8sClusters))
		} else {
			slog.Warn("failed to load infra config; explain won't auto-resolve tags",
				"path", infraPath, "err", err)
		}
	}

	return gs
}

func (s *governanceServer) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	info := GovernanceInfo{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Policy information
	if s.policyEngine != nil {
		cfg := s.policyEngine.Config()
		policies := cfg.Policies
		policySummaries := make([]PolicySummary, 0, len(policies))
		totalRules := 0

		for _, pol := range policies {
			// Build resource list
			resources := make([]string, 0, len(pol.Resources))
			for _, r := range pol.Resources {
				resources = append(resources, r.Type)
			}

			// Build rule summaries
			rules := make([]RuleSummary, 0, len(pol.Rules))
			for _, rule := range pol.Rules {
				actions := make([]string, 0, len(rule.Action))
				for _, a := range rule.Action {
					actions = append(actions, string(a))
				}

				rs := RuleSummary{
					Actions: actions,
					Effect:  string(rule.Effect),
					Message: rule.Message,
				}

				// Add conditions if present
				if rule.Conditions != nil {
					if rule.Conditions.RequireApproval {
						rs.Conditions = append(rs.Conditions, "requires approval")
					}
					if rule.Conditions.MaxRowsAffected > 0 {
						rs.Conditions = append(rs.Conditions, "row limit")
					}
					if rule.Conditions.MaxPodsAffected > 0 {
						rs.Conditions = append(rs.Conditions, "pod limit")
					}
					if rule.Conditions.Schedule != nil {
						rs.Conditions = append(rs.Conditions, "time-based")
					}
				}

				rules = append(rules, rs)
			}
			totalRules += len(pol.Rules)

			policySummaries = append(policySummaries, PolicySummary{
				Name:        pol.Name,
				Description: pol.Description,
				Enabled:     pol.IsEnabled(),
				Resources:   resources,
				Rules:       rules,
			})
		}

		info.Policy = &PolicyInfo{
			Enabled:       true,
			File:          s.policyFile,
			PoliciesCount: len(policies),
			RulesCount:    totalRules,
			Policies:      policySummaries,
		}
	} else {
		info.Policy = &PolicyInfo{
			Enabled: false,
		}
	}

	// Approval configuration
	info.Approvals = ApprovalConfig{
		Enabled:          true, // Always enabled since auditd handles approvals
		WebhookConfigured: s.notifier != nil && s.notifier.webhookURL != "",
		EmailConfigured:   s.notifier != nil && s.notifier.smtpHost != "" && len(s.notifier.emailTo) > 0,
		DefaultTimeout:   "60m", // Default from approval_handlers.go
	}

	// Get pending approval count
	if s.approvalStore != nil {
		pending, err := s.approvalStore.ListRequests(r.Context(), audit.ApprovalQueryOptions{
			Status: "pending",
			Limit:  1000,
		})
		if err == nil {
			info.Approvals.PendingCount = len(pending)
		}
	}

	// Audit status
	info.Audit = AuditStatus{
		Enabled: true,
	}

	if s.auditStore != nil {
		// Get event count and chain status
		status, err := s.auditStore.VerifyIntegrity(r.Context())
		if err == nil {
			info.Audit.EventsTotal = status.TotalEvents
			info.Audit.ChainValid = status.Valid
		}

		// Get last event timestamp
		events, err := s.auditStore.Query(r.Context(), audit.QueryOptions{Limit: 1})
		if err == nil && len(events) > 0 {
			info.Audit.LastEventAt = events[0].Timestamp.Format(time.RFC3339)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleGetPolicySummary returns a human-readable policy summary.
func (s *governanceServer) handleGetPolicySummary(w http.ResponseWriter, r *http.Request) {
	if s.policyEngine == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"enabled": false,
			"message": "No policy file configured. Set HELPDESK_POLICY_FILE to enable policy enforcement.",
		})
		return
	}

	cfg := s.policyEngine.Config()
	policySummaries := make([]map[string]any, 0, len(cfg.Policies))

	for _, pol := range cfg.Policies {
		entry := map[string]any{
			"name":    pol.Name,
			"enabled": pol.IsEnabled(),
		}
		if pol.Description != "" {
			entry["description"] = pol.Description
		}

		// Summarize resources
		resources := make([]string, 0, len(pol.Resources))
		for _, r := range pol.Resources {
			res := r.Type
			if r.Match.Name != "" {
				res += ":" + r.Match.Name
			} else if r.Match.NamePattern != "" {
				res += ":" + r.Match.NamePattern
			}
			resources = append(resources, res)
		}
		if len(resources) > 0 {
			entry["resources"] = resources
		}

		// Summarize rules
		ruleSummaries := make([]map[string]any, 0, len(pol.Rules))
		for _, rule := range pol.Rules {
			actions := make([]string, 0, len(rule.Action))
			for _, a := range rule.Action {
				actions = append(actions, string(a))
			}

			ruleEntry := map[string]any{
				"actions": actions,
				"effect":  rule.Effect,
			}
			if rule.Message != "" {
				ruleEntry["message"] = rule.Message
			}
			if rule.Conditions != nil && rule.Conditions.RequireApproval {
				ruleEntry["requires_approval"] = true
			}
			ruleSummaries = append(ruleSummaries, ruleEntry)
		}
		entry["rules"] = ruleSummaries

		policySummaries = append(policySummaries, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"enabled":     true,
		"policy_file": s.policyFile,
		"policies":    policySummaries,
	})
}

// handleExplain handles GET /v1/governance/explain — hypothetical policy check.
// It evaluates the policy engine against the supplied parameters without
// recording an audit event or executing any tool.
//
// Query parameters:
//
//	resource_type  required  "database" | "kubernetes"
//	resource_name  required  resource name (db name, namespace, …)
//	action         required  "read" | "write" | "destructive"
//	tags           optional  comma-separated tags, e.g. "production,critical"
//	user_id        optional  evaluate as a specific user
//	role           optional  evaluate with a specific role
func (s *governanceServer) handleExplain(w http.ResponseWriter, r *http.Request) {
	if s.policyEngine == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"enabled": false,
			"message": "No policy file configured. Set HELPDESK_POLICY_FILE to enable policy enforcement.",
		})
		return
	}

	q := r.URL.Query()
	resourceType := q.Get("resource_type")
	resourceName := q.Get("resource_name")
	actionStr := q.Get("action")

	if resourceType == "" || resourceName == "" || actionStr == "" {
		http.Error(w, "resource_type, resource_name and action are required", http.StatusBadRequest)
		return
	}

	var tags []string
	if raw := q.Get("tags"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}

	// When no tags were provided explicitly, try to resolve them from the infra config.
	// This mirrors how agents derive tags at runtime, so the explain result reflects
	// the same policy evaluation the agent would perform.
	if len(tags) == 0 {
		tags = s.tagsFromInfra(resourceType, resourceName)
	}

	req := policy.Request{
		Principal: policy.RequestPrincipal{
			UserID: q.Get("user_id"),
			Roles:  func() []string {
				if r := q.Get("role"); r != "" {
					return []string{r}
				}
				return nil
			}(),
		},
		Resource: policy.RequestResource{
			Type: resourceType,
			Name: resourceName,
			Tags: tags,
		},
		Action: policy.ActionClass(actionStr),
	}

	trace := s.policyEngine.Explain(req)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trace)
}

// tagsFromInfra resolves the tags for a resource from the infrastructure config.
// Returns nil when the infra config is not loaded or the resource is not found.
// writeJSONError writes a JSON {"error":"..."} body with the given status code.
// Use this instead of http.Error so that all error responses are valid JSON.
func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

func (s *governanceServer) tagsFromInfra(resourceType, resourceName string) []string {
	if s.infraConfig == nil {
		return nil
	}
	switch resourceType {
	case "database":
		if db, ok := s.infraConfig.DBServers[resourceName]; ok {
			return db.Tags
		}
	case "kubernetes":
		if k8s, ok := s.infraConfig.K8sClusters[resourceName]; ok {
			return k8s.Tags
		}
	}
	return nil
}

// PolicyCheckRequest is the body for POST /v1/governance/check.
// Agents send this instead of evaluating locally and POSTing a separate pol_* event.
type PolicyCheckRequest struct {
	ResourceType string   `json:"resource_type"` // "database" | "kubernetes"
	ResourceName string   `json:"resource_name"` // database name, namespace, etc.
	Action       string   `json:"action"`        // "read" | "write" | "destructive"
	Tags         []string `json:"tags,omitempty"`
	TraceID      string   `json:"trace_id,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	AgentName    string   `json:"agent_name,omitempty"`
	Note         string   `json:"note,omitempty"`
	// blast-radius context (post-execution checks)
	RowsAffected  int  `json:"rows_affected,omitempty"`
	PodsAffected  int  `json:"pods_affected,omitempty"`
	PostExecution bool `json:"post_execution,omitempty"`
}

// PolicyCheckResponse is returned by POST /v1/governance/check.
type PolicyCheckResponse struct {
	Effect           string               `json:"effect"`                     // allow / deny / require_approval
	PolicyName       string               `json:"policy_name"`
	Message          string               `json:"message,omitempty"`
	Explanation      string               `json:"explanation"`
	RequiresApproval bool                 `json:"requires_approval,omitempty"`
	Trace            policy.DecisionTrace `json:"trace"`
	EventID          string               `json:"event_id"`  // pol_* event recorded atomically
	TraceID          string               `json:"trace_id"`  // echoed back; chk_* prefix means auto-generated (direct call)
}

// handlePolicyCheck handles POST /v1/governance/check.
// It evaluates the policy engine and records the decision as a pol_* audit event
// atomically. Agents call this instead of running a local engine and then separately
// POSTing to /v1/events.
//
// Returns 403 Forbidden when the decision is deny; 503 when no policy engine is configured.
func (s *governanceServer) handlePolicyCheck(w http.ResponseWriter, r *http.Request) {
	if s.policyEngine == nil {
		writeJSONError(w, "policy engine not configured; set HELPDESK_POLICY_FILE and HELPDESK_POLICY_ENABLED", http.StatusServiceUnavailable)
		return
	}

	var req PolicyCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ResourceType == "" || req.ResourceName == "" || req.Action == "" {
		writeJSONError(w, "resource_type, resource_name and action are required", http.StatusBadRequest)
		return
	}

	// Agent calls must always carry a trace_id so their policy decisions are
	// traceable to the originating user request. A missing trace_id from an
	// agent indicates either an out-of-band bypass or a propagation bug —
	// reject loudly rather than silently recording an orphaned event.
	if req.AgentName != "" && req.TraceID == "" {
		writeJSONError(w, "agent requests must include trace_id", http.StatusBadRequest)
		return
	}
	// Direct calls (ops/curl) without a trace_id get a synthetic chk_* ID so
	// the event is still recorded and queryable. The chk_* prefix is exclusive
	// to calls with no agent_name — a reliable signal in the audit log.
	if req.TraceID == "" {
		req.TraceID = "chk_" + uuid.New().String()[:8]
	}

	tags := req.Tags
	// Auto-resolve tags from infra config when not supplied by the agent.
	if len(tags) == 0 {
		tags = s.tagsFromInfra(req.ResourceType, req.ResourceName)
	}

	polReq := policy.Request{
		Resource: policy.RequestResource{
			Type: req.ResourceType,
			Name: req.ResourceName,
			Tags: tags,
		},
		Action: policy.ActionClass(req.Action),
		Context: policy.RequestContext{
			TraceID:      req.TraceID,
			RowsAffected: req.RowsAffected,
			PodsAffected: req.PodsAffected,
		},
	}

	trace := s.policyEngine.Explain(polReq)
	decision := trace.Decision

	// Serialize trace for the audit record.
	traceJSON, _ := json.Marshal(trace)

	// Build and record the pol_* audit event atomically so there is exactly one
	// authoritative record — agents no longer need to POST a separate /v1/events.
	eventID := "pol_" + uuid.New().String()[:8]
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = req.TraceID // always non-empty at this point
	}

	event := &audit.Event{
		EventID:     eventID,
		Timestamp:   time.Now().UTC(),
		EventType:   audit.EventTypePolicyDecision,
		TraceID:     req.TraceID,
		ActionClass: audit.ActionClass(req.Action),
		Session:     audit.Session{ID: sessionID},
		PolicyDecision: &audit.PolicyDecision{
			ResourceType:  req.ResourceType,
			ResourceName:  req.ResourceName,
			Action:        req.Action,
			Tags:          tags,
			Effect:        string(decision.Effect),
			PolicyName:    decision.PolicyName,
			RuleIndex:     decision.RuleIndex,
			Message:       decision.Message,
			Note:          req.Note,
			PostExecution: req.PostExecution,
			Trace:         traceJSON,
			Explanation:   trace.Explanation,
		},
	}

	if err := s.auditStore.Record(r.Context(), event); err != nil {
		// Don't fail the response — policy evaluation succeeded; only persistence failed.
		slog.Error("failed to record policy check event", "event_id", eventID, "err", err)
	}

	// Log at appropriate level (mirrors handleRecordEvent).
	switch decision.Effect {
	case policy.EffectDeny:
		slog.Warn("policy check: DENY",
			"event_id", eventID,
			"resource", req.ResourceType+":"+req.ResourceName,
			"action", req.Action,
			"policy", decision.PolicyName,
			"agent", req.AgentName)
	case policy.EffectRequireApproval:
		slog.Info("policy check: REQUIRE_APPROVAL",
			"event_id", eventID,
			"resource", req.ResourceType+":"+req.ResourceName,
			"action", req.Action,
			"policy", decision.PolicyName)
	default:
		slog.Debug("policy check: ALLOW",
			"event_id", eventID,
			"resource", req.ResourceType+":"+req.ResourceName,
			"action", req.Action)
	}

	resp := PolicyCheckResponse{
		Effect:           string(decision.Effect),
		PolicyName:       decision.PolicyName,
		Message:          decision.Message,
		Explanation:      trace.Explanation,
		RequiresApproval: decision.NeedsApproval(),
		Trace:            trace,
		EventID:          eventID,
		TraceID:          req.TraceID,
	}

	httpStatus := http.StatusOK
	if decision.IsDenied() {
		httpStatus = http.StatusForbidden
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(resp)
}

// handleGetEvent handles GET /v1/events/{eventID} — retrieve a single audit event by ID.
// The event JSON includes the policy_decision.trace and policy_decision.explanation fields
// when the event was recorded by an agent using engine.Explain().
func (s *governanceServer) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	if eventID == "" {
		http.Error(w, "missing event ID", http.StatusBadRequest)
		return
	}

	events, err := s.auditStore.Query(r.Context(), audit.QueryOptions{
		EventID: eventID,
		Limit:   1,
	})
	if err != nil {
		slog.Error("failed to query event", "event_id", eventID, "err", err)
		http.Error(w, "failed to query event", http.StatusInternalServerError)
		return
	}
	if len(events) == 0 {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events[0])
}

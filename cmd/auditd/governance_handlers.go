package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/policy"
)

// governanceServer handles governance-related HTTP endpoints.
type governanceServer struct {
	auditStore    *audit.Store
	approvalStore *audit.ApprovalStore
	notifier      *ApprovalNotifier
	policyEngine  *policy.Engine
	policyFile    string
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

	// Try to load policy file if configured
	policyFile := os.Getenv("HELPDESK_POLICY_FILE")
	if policyFile != "" {
		gs.policyFile = policyFile
		cfg, err := policy.LoadFile(policyFile)
		if err == nil {
			gs.policyEngine = policy.NewEngine(policy.EngineConfig{
				PolicyConfig: cfg,
			})
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

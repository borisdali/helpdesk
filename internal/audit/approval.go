package audit

import (
	"fmt"
	"strings"
	"time"
)

// ApprovalStatus represents the state of an approval request.
type ApprovalStatus string

const (
	// ApprovalPending means the action is waiting for approval.
	ApprovalPending ApprovalStatus = "pending"

	// ApprovalApproved means the action was explicitly approved.
	ApprovalApproved ApprovalStatus = "approved"

	// ApprovalDenied means the action was explicitly denied.
	ApprovalDenied ApprovalStatus = "denied"

	// ApprovalAutoApproved means the action was auto-approved by policy.
	ApprovalAutoApproved ApprovalStatus = "auto_approved"

	// ApprovalNotRequired means no approval was needed for this action.
	ApprovalNotRequired ApprovalStatus = "not_required"
)

// Approval captures the approval chain for an action.
type Approval struct {
	// Required indicates whether this action requires approval.
	Required bool `json:"required"`

	// Status is the current approval status.
	Status ApprovalStatus `json:"status"`

	// RequestedBy is the principal who requested the action.
	RequestedBy string `json:"requested_by,omitempty"`

	// RequestedAt is when the approval was requested.
	RequestedAt time.Time `json:"requested_at,omitempty"`

	// ApprovedBy is who approved/denied the action (user ID or policy name).
	ApprovedBy string `json:"approved_by,omitempty"`

	// ApprovedAt is when the approval decision was made.
	ApprovedAt time.Time `json:"approved_at,omitempty"`

	// Justification explains why the action was taken.
	Justification string `json:"justification,omitempty"`

	// PolicyName is the policy that was applied (for auto-approval).
	PolicyName string `json:"policy_name,omitempty"`

	// ExpiresAt is when the approval expires (for time-limited approvals).
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// IsValid returns true if the approval is valid and not expired.
func (a *Approval) IsValid() bool {
	if a.Status != ApprovalApproved && a.Status != ApprovalAutoApproved {
		return false
	}
	if !a.ExpiresAt.IsZero() && time.Now().After(a.ExpiresAt) {
		return false
	}
	return true
}

// ApprovalPolicy defines rules for when approval is required.
type ApprovalPolicy struct {
	// Name identifies this policy.
	Name string `json:"name"`

	// Description explains what this policy does.
	Description string `json:"description"`

	// Rules are evaluated in order; first match wins.
	Rules []ApprovalRule `json:"rules"`
}

// ApprovalRule defines a single approval rule.
type ApprovalRule struct {
	// Name identifies this rule.
	Name string `json:"name"`

	// Match conditions (all must be true).
	ActionClasses []ActionClass `json:"action_classes,omitempty"` // match any of these
	Agents        []string      `json:"agents,omitempty"`         // match any of these
	Tools         []string      `json:"tools,omitempty"`          // match any of these
	Environments  []string      `json:"environments,omitempty"`   // match any of these (e.g., "prod")

	// Decision when rule matches.
	RequireApproval bool   `json:"require_approval"`
	AutoApprove     bool   `json:"auto_approve"` // auto-approve if true and require_approval is false
	ApproverRole    string `json:"approver_role,omitempty"` // who can approve (e.g., "admin", "dba")
}

// DefaultPolicy returns the default approval policy.
func DefaultPolicy() *ApprovalPolicy {
	return &ApprovalPolicy{
		Name:        "default",
		Description: "Default approval policy for helpdesk operations",
		Rules: []ApprovalRule{
			{
				Name:            "auto-approve-reads",
				ActionClasses:   []ActionClass{ActionRead},
				RequireApproval: false,
				AutoApprove:     true,
			},
			{
				Name:            "require-approval-destructive",
				ActionClasses:   []ActionClass{ActionDestructive},
				RequireApproval: true,
				ApproverRole:    "admin",
			},
			{
				Name:            "require-approval-prod-writes",
				ActionClasses:   []ActionClass{ActionWrite},
				Environments:    []string{"prod", "production"},
				RequireApproval: true,
				ApproverRole:    "dba",
			},
			{
				Name:            "auto-approve-non-prod-writes",
				ActionClasses:   []ActionClass{ActionWrite},
				RequireApproval: false,
				AutoApprove:     true,
			},
		},
	}
}

// ApprovalRequest contains the context for evaluating approval.
type ApprovalRequest struct {
	ActionClass ActionClass
	Agent       string
	Tool        string
	Environment string
	Principal   string
}

// Evaluate checks if approval is required and returns the appropriate Approval.
func (p *ApprovalPolicy) Evaluate(req ApprovalRequest) *Approval {
	for _, rule := range p.Rules {
		if rule.matches(req) {
			approval := &Approval{
				Required:    rule.RequireApproval,
				RequestedBy: req.Principal,
				RequestedAt: time.Now(),
				PolicyName:  p.Name + "/" + rule.Name,
			}

			if rule.RequireApproval {
				approval.Status = ApprovalPending
			} else if rule.AutoApprove {
				approval.Status = ApprovalAutoApproved
				approval.ApprovedBy = "policy:" + rule.Name
				approval.ApprovedAt = time.Now()
			} else {
				approval.Status = ApprovalNotRequired
			}

			return approval
		}
	}

	// No rule matched - default to not required
	return &Approval{
		Required:   false,
		Status:     ApprovalNotRequired,
		PolicyName: p.Name + "/default",
	}
}

// matches returns true if the rule matches the request.
func (r *ApprovalRule) matches(req ApprovalRequest) bool {
	// Check action class
	if len(r.ActionClasses) > 0 {
		matched := false
		for _, ac := range r.ActionClasses {
			if ac == req.ActionClass {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check agent
	if len(r.Agents) > 0 {
		matched := false
		for _, agent := range r.Agents {
			if agent == req.Agent {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check tool
	if len(r.Tools) > 0 {
		matched := false
		for _, tool := range r.Tools {
			if tool == req.Tool {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check environment
	if len(r.Environments) > 0 {
		matched := false
		reqEnvLower := strings.ToLower(req.Environment)
		for _, env := range r.Environments {
			if strings.ToLower(env) == reqEnvLower {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// ApprovalManager handles approval requests and decisions.
type ApprovalManager struct {
	policy          *ApprovalPolicy
	pendingApprovals map[string]*PendingApproval // keyed by event ID
}

// PendingApproval represents an action waiting for approval.
type PendingApproval struct {
	EventID     string
	TraceID     string
	Request     ApprovalRequest
	Approval    *Approval
	CreatedAt   time.Time
	Description string
}

// NewApprovalManager creates a new approval manager with the given policy.
func NewApprovalManager(policy *ApprovalPolicy) *ApprovalManager {
	if policy == nil {
		policy = DefaultPolicy()
	}
	return &ApprovalManager{
		policy:          policy,
		pendingApprovals: make(map[string]*PendingApproval),
	}
}

// CheckApproval evaluates whether an action requires approval.
func (m *ApprovalManager) CheckApproval(req ApprovalRequest) *Approval {
	return m.policy.Evaluate(req)
}

// RequestApproval creates a pending approval request.
func (m *ApprovalManager) RequestApproval(eventID, traceID, description string, req ApprovalRequest) *PendingApproval {
	approval := m.policy.Evaluate(req)

	pending := &PendingApproval{
		EventID:     eventID,
		TraceID:     traceID,
		Request:     req,
		Approval:    approval,
		CreatedAt:   time.Now(),
		Description: description,
	}

	if approval.Status == ApprovalPending {
		m.pendingApprovals[eventID] = pending
	}

	return pending
}

// Approve approves a pending request.
func (m *ApprovalManager) Approve(eventID, approverID, justification string, validFor time.Duration) error {
	pending, ok := m.pendingApprovals[eventID]
	if !ok {
		return fmt.Errorf("no pending approval for event %s", eventID)
	}

	pending.Approval.Status = ApprovalApproved
	pending.Approval.ApprovedBy = approverID
	pending.Approval.ApprovedAt = time.Now()
	pending.Approval.Justification = justification

	if validFor > 0 {
		pending.Approval.ExpiresAt = time.Now().Add(validFor)
	}

	delete(m.pendingApprovals, eventID)
	return nil
}

// Deny denies a pending request.
func (m *ApprovalManager) Deny(eventID, approverID, reason string) error {
	pending, ok := m.pendingApprovals[eventID]
	if !ok {
		return fmt.Errorf("no pending approval for event %s", eventID)
	}

	pending.Approval.Status = ApprovalDenied
	pending.Approval.ApprovedBy = approverID
	pending.Approval.ApprovedAt = time.Now()
	pending.Approval.Justification = reason

	delete(m.pendingApprovals, eventID)
	return nil
}

// GetPending returns all pending approval requests.
func (m *ApprovalManager) GetPending() []*PendingApproval {
	result := make([]*PendingApproval, 0, len(m.pendingApprovals))
	for _, p := range m.pendingApprovals {
		result = append(result, p)
	}
	return result
}

// GetPendingByID returns a specific pending approval.
func (m *ApprovalManager) GetPendingByID(eventID string) (*PendingApproval, bool) {
	p, ok := m.pendingApprovals[eventID]
	return p, ok
}

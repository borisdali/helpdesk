// Package decisions provides the unified Decision type shared between the
// gateway's Decision Hub and all notification paths (gate, fleet approval,
// per-step approval).
package decisions

import "time"

// DecisionType classifies a pending operator decision.
type DecisionType string

const (
	DecisionTypeGate          DecisionType = "gate"
	DecisionTypeFleetApproval DecisionType = "fleet_approval"
	DecisionTypeStepApproval  DecisionType = "step_approval"
)

// Decision is a normalised view of any pending operator decision, regardless
// of whether it originated as a playbook gate, a fleet job approval, or a
// per-step tool approval.
type Decision struct {
	// ID encodes the type and the underlying record identifier:
	//   "gate:{runID}"           — playbook phase-boundary gate
	//   "fleet:{approvalID}"     — fleet job write/destructive approval
	//   "step:{approvalID}"      — per-tool-call step approval inside an agent run
	ID          string       `json:"id"`
	Type        DecisionType `json:"type"`
	Status      string       `json:"status"` // "pending" | "approved" | "denied" | "expired" | "abandoned"
	Summary     string       `json:"summary"`
	RequestedBy string       `json:"requested_by"`
	RequestedAt time.Time    `json:"requested_at"`
	ExpiresAt   time.Time    `json:"expires_at,omitempty"`
	// ResolveURL is the canonical absolute URL to act on this decision.
	// For gate decisions this is POST /api/v1/decisions/gate:{runID}/resolve.
	ResolveURL string         `json:"resolve_url"`
	// Extra holds type-specific context passed through to webhook payloads
	// (e.g. findings, escalation_target, confidence_warning, tool name, args).
	Extra map[string]any `json:"extra,omitempty"`
}

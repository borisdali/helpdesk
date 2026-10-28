package audit

import "strings"

// RecoveryApproval is the recovery target a human supplied for a
// point-in-time restore, and what they approved about it. It travels from the
// gateway to the sysadmin agent in the A2A message metadata, next to the
// principal and purpose, and the agent's tools read it from the request
// context. The model never chooses these values and cannot override them: a
// restore tool that takes its target from here ignores whatever the model
// passed. Without Approved() there is no restore.
type RecoveryApproval struct {
	// Type is "time" or "lsn"; Value the target as the human gave it.
	Type  string `json:"type"`
	Value string `json:"value"`
	// PlanDigest is the plan_digest of the plan the human approved (from
	// plan_point_in_time_recovery); the restore recomputes the plan and
	// refuses if it differs. Empty before the gate: a run that only has a
	// target to plan.
	PlanDigest string `json:"plan_digest,omitempty"`
	// ApprovedBy is the operator who approved at the gate.
	ApprovedBy string `json:"approved_by,omitempty"`
	// Acknowledged is the operator's explicit acknowledgement that everything
	// committed after the target is discarded.
	Acknowledged bool `json:"acknowledged,omitempty"`
}

// Approved reports whether a restore may run on this: a target, the digest of
// the plan that was reviewed, who approved it, and the acknowledgement.
func (a *RecoveryApproval) Approved() bool {
	return a != nil && a.Type != "" && a.Value != "" && a.PlanDigest != "" && a.ApprovedBy != "" && a.Acknowledged
}

// parseRecoveryApproval reads the "recovery_approval" A2A metadata value.
// Anything that is not a well-formed object yields nil: no approval.
func parseRecoveryApproval(v any) *RecoveryApproval {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
	a := &RecoveryApproval{Type: str("type"), Value: str("value"), PlanDigest: str("plan_digest"), ApprovedBy: str("approved_by")}
	a.Acknowledged, _ = m["acknowledged"].(bool)
	if a.Type == "" || a.Value == "" {
		return nil
	}
	return a
}

// FormatRecoveryTarget is the compact form stored on a playbook run:
// "time=2026-10-27T14:05:00Z". Empty when there is no target.
func FormatRecoveryTarget(typ, value string) string {
	if typ == "" || value == "" {
		return ""
	}
	return typ + "=" + value
}

// ParseRecoveryTarget splits FormatRecoveryTarget's form.
func ParseRecoveryTarget(s string) (typ, value string) {
	typ, value, _ = strings.Cut(s, "=")
	return typ, value
}

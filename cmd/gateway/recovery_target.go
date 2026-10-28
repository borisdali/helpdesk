package main

// Point-in-time recovery targets at the gateway (v0.33).
//
// A recovery target is the one input of a restore that decides how much data
// is thrown away, so it is never the model's to choose. A human supplies it
// as a structured field (on the run request, and again at the gate with the
// digest of the plan they reviewed and an acknowledgement); Go validates it;
// and it reaches the sysadmin agent in the A2A message metadata
// (audit.RecoveryApproval), where the restore tool reads it from the request
// context and ignores whatever the model passes. Three rules, all fail
// closed:
//
//   - the playbook that restores (pitrRemediateSeries) can only be reached
//     through a gate a human approved with a target: a force-mode auto-chain
//     is stopped at that boundary whatever the approval mode, and the series
//     cannot be started directly;
//   - the target is immutable: once a run has one, an approval naming another
//     is refused;
//   - the approval needs the plan digest and the acknowledgement that
//     everything after the target is discarded.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// pitrRemediateSeries is the playbook that runs restore_to_point_in_time.
const pitrRemediateSeries = "pbs_pgbackrest_pitr_remediate"

// needsRecoveryApproval reports whether a playbook restores to a recovery
// target, and so must not run without a human's approval of one.
func needsRecoveryApproval(seriesID string) bool { return seriesID == pitrRemediateSeries }

// gateReasonRecoveryTarget is the gate reason raised on the boundary into a
// point-in-time restore when no human has approved a target.
const gateReasonRecoveryTarget = "recovery_target_required"

// RecoveryTargetSpec is a recovery target as a human gives it.
type RecoveryTargetSpec struct {
	// Type is "time" or "lsn".
	Type string `json:"type"`
	// Value is an RFC3339 timestamp with a time zone, or an LSN (X/Y).
	Value string `json:"value"`
}

var (
	recoveryLSNRe    = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)
	recoveryDigestRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Validate checks the shape of the target. The agent re-validates it (and
// refuses a future time) before using it; this keeps a malformed one out of
// the audit trail and the A2A metadata.
func (s *RecoveryTargetSpec) Validate() error {
	if s == nil {
		return fmt.Errorf("recovery_target is required")
	}
	v := strings.TrimSpace(s.Value)
	switch s.Type {
	case "time":
		if len(v) > 40 {
			return fmt.Errorf("recovery_target.value is too long")
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return fmt.Errorf("recovery_target.value %q is not an RFC3339 timestamp with a time zone (e.g. 2026-10-27T14:05:00Z)", s.Value)
		}
	case "lsn":
		if !recoveryLSNRe.MatchString(v) {
			return fmt.Errorf("recovery_target.value %q is not an LSN (e.g. 0/3000028)", s.Value)
		}
	case "":
		return fmt.Errorf("recovery_target.type is required (time or lsn)")
	default:
		return fmt.Errorf("recovery_target.type %q is not supported (time or lsn)", s.Type)
	}
	s.Value = v
	return nil
}

// stored is the compact form kept on a playbook run.
func (s *RecoveryTargetSpec) stored() string {
	if s == nil {
		return ""
	}
	return audit.FormatRecoveryTarget(s.Type, s.Value)
}

// planOnly is the approval a run carries when it only has a target to plan.
func (s *RecoveryTargetSpec) planOnly() *audit.RecoveryApproval {
	if s == nil {
		return nil
	}
	return &audit.RecoveryApproval{Type: s.Type, Value: s.Value}
}

// recoveryApprovalFromGate validates a gate approval for the hop that
// restores to a recovery target and returns what the agent will be given. A
// non-zero status is a refusal, with the message for the caller.
//
// stored is the target the run already has ("" if none): the approval may
// restate it but not change it.
func recoveryApprovalFromGate(req ProceedEscalationRequest, stored, resolvedBy string) (*audit.RecoveryApproval, int, string) {
	if err := req.RecoveryTarget.Validate(); err != nil {
		return nil, http.StatusBadRequest, "approving a point-in-time restore needs the recovery target: " + err.Error()
	}
	if !recoveryDigestRe.MatchString(strings.TrimSpace(req.PlanDigest)) {
		return nil, http.StatusBadRequest, "approving a point-in-time restore needs plan_digest: the 16-character plan_digest of the plan_point_in_time_recovery result you reviewed"
	}
	if !req.AcknowledgeDataLoss {
		return nil, http.StatusBadRequest, "approving a point-in-time restore needs acknowledge_data_loss=true: everything committed after the target is discarded"
	}
	if stored != "" && stored != req.RecoveryTarget.stored() {
		return nil, http.StatusConflict, fmt.Sprintf("recovery_target %s is not the target this run was started with (%s): a recovery target cannot be changed once set. Start a new run for the other target", req.RecoveryTarget.stored(), stored)
	}
	return &audit.RecoveryApproval{
		Type:         req.RecoveryTarget.Type,
		Value:        req.RecoveryTarget.Value,
		PlanDigest:   strings.TrimSpace(req.PlanDigest),
		ApprovedBy:   resolvedBy,
		Acknowledged: true,
		StopServing:  req.AllowStopServing,
	}, 0, ""
}

// recoveryAuditLines is what the gate_acknowledged event says about a
// point-in-time approval: who approved which target, against which plan, and
// whether they also authorized stopping the running instance.
func recoveryAuditLines(ap *audit.RecoveryApproval) []string {
	lines := []string{
		"recovery_target: " + audit.FormatRecoveryTarget(ap.Type, ap.Value),
		"plan_digest: " + ap.PlanDigest,
		"operator acknowledged that everything committed after the target is discarded",
	}
	if ap.StopServing {
		lines = append(lines, "operator authorized stopping the running instance for the restore")
	}
	return lines
}

// recoveryApprovalMeta is the A2A metadata form of an approval.
func recoveryApprovalMeta(a *audit.RecoveryApproval) map[string]any {
	if a == nil {
		return nil
	}
	m := map[string]any{"type": a.Type, "value": a.Value}
	if a.PlanDigest != "" {
		m["plan_digest"] = a.PlanDigest
	}
	if a.ApprovedBy != "" {
		m["approved_by"] = a.ApprovedBy
	}
	if a.Acknowledged {
		m["acknowledged"] = true
	}
	if a.StopServing {
		m["stop_serving"] = true
	}
	return m
}

// ctxKeyRecoveryApproval carries the approval from the playbook handler to
// the proxy that builds the A2A metadata.
type ctxKeyRecoveryApprovalType struct{}

var ctxKeyRecoveryApproval = ctxKeyRecoveryApprovalType{}

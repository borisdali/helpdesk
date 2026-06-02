package main

import (
	"context"

	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// ctxKeyFaultTraceID is the context key for the per-fault X-Trace-ID value
// stored by main.go and remediation.go. The wrapper bridges it into faultlib's
// context slot so that faultlib.Runner can set the X-Trace-ID header.
type ctxKeyFaultTraceID struct{}

// Runner wraps faultlib.Runner, adapting the local Failure and HarnessConfig
// types so that package main does not duplicate runner logic.
type Runner struct {
	inner *faultlib.Runner
}

// NewRunner creates a Runner backed by cfg.
func NewRunner(cfg *HarnessConfig) *Runner {
	return &Runner{inner: faultlib.NewRunner(toLFConfig(cfg))}
}

// Run sends the failure prompt to the appropriate agent and returns the response.
func (r *Runner) Run(ctx context.Context, f Failure) testutil.AgentResponse {
	// Bridge the local trace-ID context slot into faultlib's slot so that
	// faultlib.Runner sets the X-Trace-ID header on gateway requests.
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
		ctx = faultlib.WithFaultTraceID(ctx, id)
	}
	return r.inner.Run(ctx, toLFFailure(f))
}

// toLFConfig converts a local HarnessConfig to faultlib.HarnessConfig.
func toLFConfig(cfg *HarnessConfig) *faultlib.HarnessConfig {
	return &faultlib.HarnessConfig{
		CatalogPath:      cfg.CatalogPath,
		TestingDir:       cfg.TestingDir,
		ConnStr:          cfg.ConnStr,
		ReplicaConnStr:   cfg.ReplicaConnStr,
		AgentConnStr:     cfg.AgentConnStr,
		DBAgentURL:       cfg.DBAgentURL,
		K8sAgentURL:      cfg.K8sAgentURL,
		SysadminAgentURL: cfg.SysadminAgentURL,
		OrchestratorURL:  cfg.OrchestratorURL,
		KubeContext:      cfg.KubeContext,
		Categories:       cfg.Categories,
		FailureIDs:       cfg.FailureIDs,
		External:         cfg.External,
		RemediateEnabled: cfg.RemediateEnabled,
		GatewayURL:       cfg.GatewayURL,
		GatewayAPIKey:    cfg.GatewayAPIKey,
		GatewayPurpose:   cfg.GatewayPurpose,
		InfraConfigPath:  cfg.InfraConfigPath,
		ServerID:         faultlib.ResolveServerID(cfg.ConnStr, cfg.InfraConfigPath),
		SSHHost:          cfg.SSHHost,
		SSHUser:          cfg.SSHUser,
		SSHKeyPath:       cfg.SSHKeyPath,
		CustomCatalogs:   cfg.CustomCatalogs,
		SourceFilter:     cfg.SourceFilter,
		ViaGateway:       cfg.ViaGateway,
		ApprovalMode:     cfg.ApprovalMode,
		OperatorID:       cfg.OperatorID,
		AuditURL:         cfg.AuditURL,
		ReportDir:        cfg.ReportDir,
		JudgeEnabled:     cfg.JudgeEnabled,
		JudgeModel:       cfg.JudgeModel,
		JudgeVendor:      cfg.JudgeVendor,
		JudgeAPIKey:      cfg.JudgeAPIKey,
		NotifyURL:      cfg.NotifyURL,
		GateEscalation: cfg.GateEscalation,
		EmitAndWait:    cfg.EmitAndWait,
	}
}

// toLFFailure converts a local Failure to faultlib.Failure.
// Only the fields consumed by faultlib.Runner.Run are populated.
func toLFFailure(f Failure) faultlib.Failure {
	return faultlib.Failure{
		ID:                        f.ID,
		Category:                  f.Category,
		Prompt:                    f.Prompt,
		Timeout:                   f.Timeout,
		DiagnosisPlaybookSeriesID: f.DiagnosisPlaybookSeriesID,
	}
}

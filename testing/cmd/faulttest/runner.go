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
	// f is already a faultlib.Failure (Failure is a type alias — item 7
	// dedup, v0.26); no conversion needed.
	return r.inner.Run(ctx, f)
}

// toLFConfig returns a live pointer to cfg's embedded faultlib.HarnessConfig
// (HarnessConfig embeds it — item 7 dedup, v0.26). Deliberately NOT a value
// copy: Runner/Remediator are constructed once, before the fault loop starts
// (see main.go), and a value copy would freeze their view of cfg at that
// moment — silently invisible to any later mutation, including the
// execConfig-type faults (db-auth-failure, db-not-exist) that intentionally
// swap ConnStr to a broken DSN mid-run. That was a real, live bug before
// this change: Injector already rebuilds fresh on every Inject/Teardown call
// specifically to see such mutations, but Runner/Remediator's own
// hand-mapped copies never did, so faultlib.Runner.Run's ConnStr-based
// agentConn fallback (faultlib/runner.go) could silently use the
// pre-injection connection string instead of the fault's intended broken
// one, whenever --agent-conn wasn't separately set. A live pointer means
// every caller — Injector's per-call rebuild, Runner/Remediator's
// constructed-once instances — now observes the same, always-current state.
//
// ServerID is resolved here (not stored on cfg directly, matching the
// original mapping's behavior) since it depends on ConnStr/InfraConfigPath,
// which can change between calls for the same reason above.
func toLFConfig(cfg *HarnessConfig) *faultlib.HarnessConfig {
	cfg.ServerID = faultlib.ResolveServerID(cfg.ConnStr, cfg.InfraConfigPath)
	return &cfg.HarnessConfig
}

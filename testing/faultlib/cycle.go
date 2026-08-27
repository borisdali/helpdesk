package faultlib

import (
	"context"
	"time"

	"helpdesk/testing/testutil"
)

// FaultInjector is the minimal interface RunFaultCycle/TeardownFault need
// from an injector. Both *Injector (this package) and cmd/faulttest's own
// wrapper (which rebuilds a fresh faultlib.Injector on every call to observe
// execConfig-type ConnStr mutations) satisfy this structurally — no explicit
// implements needed.
type FaultInjector interface {
	Inject(ctx context.Context, f Failure) error
	Teardown(ctx context.Context, f Failure) error
}

// FaultRunner is the minimal interface RunFaultCycle needs from a runner.
type FaultRunner interface {
	Run(ctx context.Context, f Failure) testutil.AgentResponse
}

// DefaultTeardownTimeout is the budget given to every fault's teardown
// phase, under its own fresh context (not derived from the caller's ctx) —
// see TeardownFault. No catalog fault's teardown currently needs more than
// this (even k8s-node-memory-pressure's is a simple idempotent kubectl
// delete); revisit as a per-fault override only if one ever does.
const DefaultTeardownTimeout = 60 * time.Second

// TeardownFault runs f's teardown under its own fresh
// context.WithTimeout(context.Background(), DefaultTeardownTimeout) rather
// than a context derived from the caller's ctx — so teardown still gets a
// full budget even when the caller's own context has already expired or been
// cancelled (e.g. the run phase's ctx already timed out). Callers own WHEN
// to invoke this (RunFaultCycle calls it automatically on injection failure;
// callers that also run remediation between Run and teardown — the fault
// must stay injected until remediation completes — call it themselves,
// afterward).
func TeardownFault(injector FaultInjector, f Failure) error {
	tdCtx, cancel := context.WithTimeout(context.Background(), DefaultTeardownTimeout)
	defer cancel()
	return injector.Teardown(tdCtx, f)
}

// FaultCycleResult holds the outcome of one fault's inject→run phase.
type FaultCycleResult struct {
	Response  testutil.AgentResponse
	InjectErr error
	// TeardownErr is set only when InjectErr is also set — RunFaultCycle
	// tears down automatically on injection failure (there is nothing for a
	// later remediation step to act on, so there's no reason to delay it).
	// On injection success, TeardownErr is always nil here; the caller owns
	// calling TeardownFault itself once it's actually safe to tear down
	// (immediately, or after remediation completes).
	TeardownErr error
}

// RunFaultCycle runs the inject→run phase of one fault, giving injection its
// own bounded context instead of relying on one unbounded context and an
// outer process-level -timeout as the only safety net (Part B, v0.26 — see
// project memory for the full "why now" story: this was deliberately
// sequenced after item 7's type/config/evaluator dedup so the timeout logic
// only needs to be written once, not twice). Run is not separately wrapped
// here — Runner.Run already self-bounds via f.TimeoutDuration().
//
// When Inject fails or times out, Teardown runs automatically (via
// TeardownFault) before RunFaultCycle returns — closing a bug that used to
// be hand-rolled, inconsistently, in each entry point: TestFaultInjection
// registered its teardown defer before checking the Inject error;
// TestExternalModeInjection did not (confirmed live gap, fixed by this
// helper); cmd/faulttest's CLI loop added an explicit Teardown call in the
// injection-error branch. One correct implementation now covers all of them.
//
// When Inject succeeds, Teardown is deliberately NOT run automatically —
// callers that also attempt remediation (cmd/faulttest's CLI, and
// TestFaultInjection's own optional remediation phase) need the fault to
// remain injected until remediation completes, not torn down the instant
// the diagnosis call returns. Call TeardownFault yourself once it's safe.
//
// beforeTeardown, when non-nil, runs immediately before the automatic
// injection-failure Teardown call — callers use it to reset any
// HarnessConfig mutation Inject made (e.g. execConfig-type faults like
// db-auth-failure swap in a bad ConnStr; the fault's own teardown spec
// relies on the caller having already restored the original value, since
// {type: config, restore: true} does not itself perform a restore — see
// execConfig). Callers doing their own later TeardownFault call on the
// success path are responsible for the same reset themselves.
//
// traceID, when non-empty, is attached to the context passed to Run only
// (not Inject) via WithFaultTraceID, matching both entry points' existing
// behavior.
func RunFaultCycle(ctx context.Context, injector FaultInjector, runner FaultRunner, f Failure, traceID string, beforeTeardown func()) FaultCycleResult {
	var result FaultCycleResult

	injectCtx, cancel := context.WithTimeout(ctx, f.InjectTimeoutDuration())
	defer cancel()
	if err := injector.Inject(injectCtx, f); err != nil {
		result.InjectErr = err
		if beforeTeardown != nil {
			beforeTeardown()
		}
		result.TeardownErr = TeardownFault(injector, f)
		return result
	}

	runCtx := ctx
	if traceID != "" {
		runCtx = WithFaultTraceID(ctx, traceID)
	}
	result.Response = runner.Run(runCtx, f)
	return result
}

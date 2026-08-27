package faultlib

import (
	"context"
	"errors"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

// mockCycleInjector is a test double for FaultInjector that records calls
// and can be configured to fail or block.
type mockCycleInjector struct {
	injectErr        error
	injectDelay      time.Duration
	teardownErr      error
	injectCalls      int
	teardownCalls    int
	sawInjectCtxDone bool
}

func (m *mockCycleInjector) Inject(ctx context.Context, f Failure) error {
	m.injectCalls++
	if m.injectDelay > 0 {
		select {
		case <-time.After(m.injectDelay):
		case <-ctx.Done():
			m.sawInjectCtxDone = true
			return ctx.Err()
		}
	}
	return m.injectErr
}

func (m *mockCycleInjector) Teardown(ctx context.Context, f Failure) error {
	m.teardownCalls++
	return m.teardownErr
}

// mockCycleRunner is a test double for FaultRunner.
type mockCycleRunner struct {
	resp       testutil.AgentResponse
	runCalls   int
	sawTraceID string
}

func (m *mockCycleRunner) Run(ctx context.Context, f Failure) testutil.AgentResponse {
	m.runCalls++
	m.sawTraceID = FaultTraceID(ctx)
	return m.resp
}

func TestRunFaultCycle_Success(t *testing.T) {
	injector := &mockCycleInjector{}
	runner := &mockCycleRunner{resp: testutil.AgentResponse{Text: "diagnosed"}}
	f := Failure{ID: "test-fault"}

	beforeTeardownCalled := false
	result := RunFaultCycle(context.Background(), injector, runner, f, "trace-123", func() {
		beforeTeardownCalled = true
	})

	if result.InjectErr != nil {
		t.Errorf("InjectErr = %v, want nil", result.InjectErr)
	}
	if result.TeardownErr != nil {
		t.Errorf("TeardownErr = %v, want nil on success (caller owns teardown timing)", result.TeardownErr)
	}
	if result.Response.Text != "diagnosed" {
		t.Errorf("Response.Text = %q, want %q", result.Response.Text, "diagnosed")
	}
	if injector.teardownCalls != 0 {
		t.Errorf("teardownCalls = %d, want 0 — RunFaultCycle must not tear down on injection success "+
			"(remediation needs the fault to remain injected)", injector.teardownCalls)
	}
	if beforeTeardownCalled {
		t.Error("beforeTeardown should not run when injection succeeds")
	}
	if runner.sawTraceID != "trace-123" {
		t.Errorf("Run saw trace ID %q, want %q", runner.sawTraceID, "trace-123")
	}
}

func TestRunFaultCycle_InjectFails_TearsDownAutomatically(t *testing.T) {
	injectErr := errors.New("injection boom")
	injector := &mockCycleInjector{injectErr: injectErr}
	runner := &mockCycleRunner{}
	f := Failure{ID: "test-fault"}

	beforeTeardownCalled := false
	result := RunFaultCycle(context.Background(), injector, runner, f, "trace-123", func() {
		beforeTeardownCalled = true
	})

	if result.InjectErr != injectErr {
		t.Errorf("InjectErr = %v, want %v", result.InjectErr, injectErr)
	}
	if runner.runCalls != 0 {
		t.Error("Run should never be called when Inject fails")
	}
	if injector.teardownCalls != 1 {
		t.Errorf("teardownCalls = %d, want 1 — teardown must run automatically on injection failure "+
			"(the bug this closes: a failed injection previously stranded whatever it had already "+
			"created, confirmed live with k8s-node-memory-pressure)", injector.teardownCalls)
	}
	if !beforeTeardownCalled {
		t.Error("beforeTeardown should run before the automatic teardown on injection failure")
	}
	if result.TeardownErr != nil {
		t.Errorf("TeardownErr = %v, want nil", result.TeardownErr)
	}
}

func TestRunFaultCycle_InjectFails_TeardownErrorSurfaced(t *testing.T) {
	injector := &mockCycleInjector{
		injectErr:   errors.New("inject boom"),
		teardownErr: errors.New("teardown also boom"),
	}
	runner := &mockCycleRunner{}
	f := Failure{ID: "test-fault"}

	result := RunFaultCycle(context.Background(), injector, runner, f, "", nil)

	if result.InjectErr == nil {
		t.Error("InjectErr should be set")
	}
	if result.TeardownErr == nil {
		t.Error("TeardownErr should be surfaced even though InjectErr is also set — both failures matter")
	}
}

func TestRunFaultCycle_InjectTimeout(t *testing.T) {
	// f.InjectTimeoutDuration() defaults to 90s, far longer than this test
	// should take — override via a fault whose InjectTimeout is tiny so the
	// context genuinely expires mid-injection.
	injector := &mockCycleInjector{injectDelay: 50 * time.Millisecond}
	runner := &mockCycleRunner{}
	f := Failure{ID: "test-fault", InjectTimeout: "10ms"}

	result := RunFaultCycle(context.Background(), injector, runner, f, "", nil)

	if result.InjectErr == nil {
		t.Fatal("InjectErr should be set when injection exceeds InjectTimeoutDuration")
	}
	if !errors.Is(result.InjectErr, context.DeadlineExceeded) {
		t.Errorf("InjectErr = %v, want context.DeadlineExceeded", result.InjectErr)
	}
	if !injector.sawInjectCtxDone {
		t.Error("injector should have observed its context's Done channel firing")
	}
	if injector.teardownCalls != 1 {
		t.Error("teardown must still run automatically after an injection timeout, same as any other injection error")
	}
}

func TestRunFaultCycle_NoTraceID(t *testing.T) {
	injector := &mockCycleInjector{}
	runner := &mockCycleRunner{}
	f := Failure{ID: "test-fault"}

	RunFaultCycle(context.Background(), injector, runner, f, "", nil)

	if runner.sawTraceID != "" {
		t.Errorf("Run saw trace ID %q, want empty when traceID param is empty", runner.sawTraceID)
	}
}

func TestTeardownFault(t *testing.T) {
	injector := &mockCycleInjector{}
	f := Failure{ID: "test-fault"}

	if err := TeardownFault(injector, f); err != nil {
		t.Errorf("TeardownFault() = %v, want nil", err)
	}
	if injector.teardownCalls != 1 {
		t.Errorf("teardownCalls = %d, want 1", injector.teardownCalls)
	}
}

func TestTeardownFault_UsesFreshContextNotCallerContext(t *testing.T) {
	// TeardownFault must give teardown a full budget even when called after
	// the caller's own context has already been cancelled — that's the
	// entire point of deriving from context.Background() instead of
	// accepting a ctx parameter. Simulate by having the injector check
	// whether it received a live (non-cancelled) context.
	injector := &contextCheckingInjector{}
	f := Failure{ID: "test-fault"}

	// Prove the point: even though nothing in this test cancels any
	// context, TeardownFault's signature doesn't accept one at all — the
	// only context reaching Teardown is TeardownFault's own fresh one.
	if err := TeardownFault(injector, f); err != nil {
		t.Errorf("TeardownFault() = %v, want nil", err)
	}
	if !injector.gotLiveContext {
		t.Error("Teardown should have received a live, non-cancelled context")
	}
}

type contextCheckingInjector struct {
	gotLiveContext bool
}

func (c *contextCheckingInjector) Inject(ctx context.Context, f Failure) error { return nil }

func (c *contextCheckingInjector) Teardown(ctx context.Context, f Failure) error {
	select {
	case <-ctx.Done():
		c.gotLiveContext = false
	default:
		c.gotLiveContext = true
	}
	return nil
}

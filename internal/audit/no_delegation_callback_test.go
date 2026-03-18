package audit

import (
	"context"
	"testing"

	"google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// --- DelegationGuard unit tests ---

func TestDelegationGuard_MarkAndCheck(t *testing.T) {
	g := NewDelegationGuard()

	if g.WasCalled("inv-1") {
		t.Error("WasCalled = true before MarkCalled")
	}

	g.MarkCalled("inv-1")

	if !g.WasCalled("inv-1") {
		t.Error("WasCalled = false after MarkCalled")
	}
	// Different invocation is unaffected.
	if g.WasCalled("inv-2") {
		t.Error("WasCalled = true for unrelated invocation ID")
	}
}

func TestDelegationGuard_IncrementRetry(t *testing.T) {
	g := NewDelegationGuard()

	if n := g.RetryCount("inv-1"); n != 0 {
		t.Errorf("initial RetryCount = %d, want 0", n)
	}
	if n := g.IncrementRetry("inv-1"); n != 1 {
		t.Errorf("first IncrementRetry = %d, want 1", n)
	}
	if n := g.IncrementRetry("inv-1"); n != 2 {
		t.Errorf("second IncrementRetry = %d, want 2", n)
	}
	if n := g.RetryCount("inv-1"); n != 2 {
		t.Errorf("RetryCount after 2 increments = %d, want 2", n)
	}
	// Separate invocation starts at zero.
	if n := g.RetryCount("inv-2"); n != 0 {
		t.Errorf("inv-2 RetryCount = %d, want 0", n)
	}
}

func TestDelegationGuard_Reset(t *testing.T) {
	g := NewDelegationGuard()
	g.MarkCalled("inv-1")
	g.IncrementRetry("inv-1")
	g.Reset("inv-1")

	if g.WasCalled("inv-1") {
		t.Error("WasCalled = true after Reset")
	}
	if n := g.RetryCount("inv-1"); n != 0 {
		t.Errorf("RetryCount = %d after Reset, want 0", n)
	}
}

// --- NoDelegationCallback unit tests ---

// mockCallbackCtx satisfies agent.CallbackContext for testing.
// It embeds context.Context to satisfy the Deadline/Done/Err/Value methods.
type mockCallbackCtx struct {
	context.Context
	invocationID string
}

func (m mockCallbackCtx) InvocationID() string                 { return m.invocationID }
func (m mockCallbackCtx) UserContent() *genai.Content          { return nil }
func (m mockCallbackCtx) AgentName() string                    { return "test-agent" }
func (m mockCallbackCtx) ReadonlyState() session.ReadonlyState { return nil }
func (m mockCallbackCtx) UserID() string                       { return "test-user" }
func (m mockCallbackCtx) AppName() string                      { return "test-app" }
func (m mockCallbackCtx) SessionID() string                    { return "test-session" }
func (m mockCallbackCtx) Branch() string                       { return "" }
func (m mockCallbackCtx) Artifacts() agent.Artifacts           { return nil }
func (m mockCallbackCtx) State() session.State                 { return nil }

var _ agent.CallbackContext = mockCallbackCtx{}

// textOnlyResponse returns a *model.LLMResponse with a single text part.
func textOnlyResponse(text string) *adkmodel.LLMResponse {
	return &adkmodel.LLMResponse{
		Content: &genai.Content{
			Role: "model",
			Parts: []*genai.Part{{Text: text}},
		},
	}
}

// functionCallResponse returns a *model.LLMResponse with a function call part.
func functionCallResponse(name string) *adkmodel.LLMResponse {
	return &adkmodel.LLMResponse{
		Content: &genai.Content{
			Role: "model",
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: name}},
			},
		},
	}
}

func TestNoDelegationCallback_NoOpWhenDelegateWasCalled(t *testing.T) {
	guard := NewDelegationGuard()
	guard.MarkCalled("inv-1")

	cb := NoDelegationCallback(guard, 2)
	ctx := mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}
	resp := textOnlyResponse("I have terminated the pod.")

	got, err := cb(ctx, resp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil response when delegate was called, got non-nil")
	}
}

func TestNoDelegationCallback_NoOpForFunctionCallResponse(t *testing.T) {
	guard := NewDelegationGuard()

	cb := NoDelegationCallback(guard, 2)
	ctx := mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}
	resp := functionCallResponse("delegate_to_agent")

	got, err := cb(ctx, resp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil response for function-call LLM response, got non-nil")
	}
}

func TestNoDelegationCallback_InjectsCorrectionOnFirstDetection(t *testing.T) {
	guard := NewDelegationGuard()

	cb := NoDelegationCallback(guard, 2)
	ctx := mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}
	resp := textOnlyResponse("I have terminated the pod.")

	got, err := cb(ctx, resp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected injected response, got nil")
	}

	// Injected response must contain a function call for the correction tool.
	if got.Content == nil || len(got.Content.Parts) == 0 {
		t.Fatal("injected response has no content parts")
	}
	fc := got.Content.Parts[0].FunctionCall
	if fc == nil {
		t.Fatal("injected response part is not a FunctionCall")
	}
	if fc.Name != CorrectionToolName {
		t.Errorf("FunctionCall.Name = %q, want %q", fc.Name, CorrectionToolName)
	}

	// Guard retry count should have incremented.
	if n := guard.RetryCount("inv-1"); n != 1 {
		t.Errorf("RetryCount = %d after first detection, want 1", n)
	}
}

func TestNoDelegationCallback_SuppressesAfterMaxRetries(t *testing.T) {
	guard := NewDelegationGuard()

	cb := NoDelegationCallback(guard, 2)
	ctx := mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}
	resp := textOnlyResponse("I have terminated the pod.")

	// First two calls inject corrections.
	for i := 1; i <= 2; i++ {
		got, err := cb(ctx, resp, nil)
		if err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", i, err)
		}
		if got == nil || got.Content == nil || got.Content.Parts[0].FunctionCall == nil {
			t.Fatalf("attempt %d: expected correction injection, got %v", i, got)
		}
	}

	// Third call (maxRetries+1) should return the suppressed response text.
	got, err := cb(ctx, resp, nil)
	if err != nil {
		t.Fatalf("suppression call: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected suppressed response, got nil")
	}
	if got.Content == nil || len(got.Content.Parts) == 0 {
		t.Fatal("suppressed response has no content")
	}
	// Suppressed response must be text (not a function call).
	if got.Content.Parts[0].FunctionCall != nil {
		t.Error("suppressed response unexpectedly contains a function call")
	}
	if got.Content.Parts[0].Text == "" {
		t.Error("suppressed response text is empty")
	}
}

func TestNoDelegationCallback_DefaultMaxRetries(t *testing.T) {
	guard := NewDelegationGuard()
	// maxRetries=0 should use DefaultMaxDelegationRetries.
	cb := NoDelegationCallback(guard, 0)
	ctx := mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}
	resp := textOnlyResponse("I have done the thing.")

	// Exhaust default retries.
	for i := 0; i < DefaultMaxDelegationRetries; i++ {
		got, err := cb(ctx, resp, nil)
		if err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", i+1, err)
		}
		if got == nil || got.Content.Parts[0].FunctionCall == nil {
			t.Fatalf("attempt %d: expected correction injection", i+1)
		}
	}

	// Next call should suppress.
	got, err := cb(ctx, resp, nil)
	if err != nil {
		t.Fatalf("suppression: %v", err)
	}
	if got == nil || got.Content.Parts[0].FunctionCall != nil {
		t.Error("expected suppressed text response after default max retries")
	}
}

func TestNoDelegationCallback_NilResponse(t *testing.T) {
	guard := NewDelegationGuard()
	cb := NoDelegationCallback(guard, 2)
	ctx := mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}

	got, err := cb(ctx, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nil LLMResponse, got non-nil")
	}
}

func TestNoDelegationCallback_PerInvocationIsolation(t *testing.T) {
	guard := NewDelegationGuard()
	cb := NoDelegationCallback(guard, 2)
	resp := textOnlyResponse("I did something.")

	// inv-1: exhaust retries.
	for i := 0; i < 2; i++ {
		cb(mockCallbackCtx{Context: context.Background(), invocationID: "inv-1"}, resp, nil) //nolint:errcheck
	}

	// inv-2 should start fresh with retry count 0 → inject correction.
	got, err := cb(mockCallbackCtx{Context: context.Background(), invocationID: "inv-2"}, resp, nil)
	if err != nil {
		t.Fatalf("inv-2: unexpected error: %v", err)
	}
	if got == nil || got.Content.Parts[0].FunctionCall == nil {
		t.Error("inv-2 expected correction injection but got suppressed/nil")
	}
}

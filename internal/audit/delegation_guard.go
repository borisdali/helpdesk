package audit

import "sync"

// DelegationGuard tracks whether delegate_to_agent was called during an ADK
// invocation. It is shared between the DelegateTool closure (which calls
// MarkCalled) and NoDelegationCallback (which reads WasCalled to decide
// whether to inject a correction). Invocation IDs are UUIDs so separate
// invocations never share state; Reset is provided for long-running processes
// that want to bound memory usage.
type DelegationGuard struct {
	mu      sync.Mutex
	called  map[string]bool
	retries map[string]int
}

// NewDelegationGuard allocates a ready-to-use guard.
func NewDelegationGuard() *DelegationGuard {
	return &DelegationGuard{
		called:  make(map[string]bool),
		retries: make(map[string]int),
	}
}

// MarkCalled records that delegate_to_agent executed for this invocation.
func (g *DelegationGuard) MarkCalled(invocationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.called[invocationID] = true
}

// WasCalled reports whether delegate_to_agent ran for this invocation.
func (g *DelegationGuard) WasCalled(invocationID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.called[invocationID]
}

// IncrementRetry increments the correction-injection retry counter for this
// invocation and returns the new count.
func (g *DelegationGuard) IncrementRetry(invocationID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.retries[invocationID]++
	return g.retries[invocationID]
}

// RetryCount returns the current correction retry count without incrementing.
func (g *DelegationGuard) RetryCount(invocationID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.retries[invocationID]
}

// Reset clears all state for the given invocation. Callers need not call Reset
// for correctness (invocation IDs are unique), but may do so to bound memory.
func (g *DelegationGuard) Reset(invocationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.called, invocationID)
	delete(g.retries, invocationID)
}

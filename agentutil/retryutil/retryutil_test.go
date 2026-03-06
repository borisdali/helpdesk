package retryutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

// zeroDelay is a Config that skips all waits — safe for unit tests.
var zeroDelay = Config{
	MaxAttempts:   3,
	InitialDelay:  0,
	MaxDelay:      0,
	BackoffFactor: 1,
	Jitter:        false,
}

func TestWaitUntilResolved_FirstAttempt(t *testing.T) {
	calls := 0
	resolved, attempts, err := WaitUntilResolved(context.Background(), zeroDelay,
		func() (bool, error) { calls++; return true, nil },
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resolved {
		t.Error("expected resolved=true")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if calls != 1 {
		t.Errorf("check called %d times, want 1", calls)
	}
}

func TestWaitUntilResolved_ThirdAttempt(t *testing.T) {
	calls := 0
	resolved, attempts, err := WaitUntilResolved(context.Background(), zeroDelay,
		func() (bool, error) {
			calls++
			return calls >= 3, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resolved {
		t.Error("expected resolved=true")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestWaitUntilResolved_Exhausted(t *testing.T) {
	cfg := Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	resolved, attempts, err := WaitUntilResolved(context.Background(), cfg,
		func() (bool, error) { return false, nil },
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved {
		t.Error("expected resolved=false")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestWaitUntilResolved_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	resolved, attempts, err := WaitUntilResolved(ctx, zeroDelay,
		func() (bool, error) { return false, nil },
		nil,
	)
	if err == nil {
		t.Error("expected context error, got nil")
	}
	if resolved {
		t.Error("expected resolved=false on cancellation")
	}
	_ = attempts // may be 0 if ctx cancelled before first check
}

func TestWaitUntilResolved_CheckError(t *testing.T) {
	sentinel := errors.New("db error")
	resolved, attempts, err := WaitUntilResolved(context.Background(), zeroDelay,
		func() (bool, error) { return false, sentinel },
		nil,
	)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
	if resolved {
		t.Error("expected resolved=false on check error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestWaitUntilResolved_AfterAttemptCallback(t *testing.T) {
	var gotAttempts []int
	var gotResolved []bool

	cfg := Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	calls := 0
	WaitUntilResolved(context.Background(), cfg,
		func() (bool, error) { calls++; return calls == 3, nil },
		func(attempt int, resolved bool) {
			gotAttempts = append(gotAttempts, attempt)
			gotResolved = append(gotResolved, resolved)
		},
	)

	wantAttempts := []int{1, 2, 3}
	wantResolved := []bool{false, false, true}
	for i := range wantAttempts {
		if i >= len(gotAttempts) || gotAttempts[i] != wantAttempts[i] {
			t.Errorf("gotAttempts[%d] = %v, want %v", i, gotAttempts, wantAttempts)
			break
		}
		if i >= len(gotResolved) || gotResolved[i] != wantResolved[i] {
			t.Errorf("gotResolved[%d] = %v, want %v", i, gotResolved, wantResolved)
			break
		}
	}
}

func TestNextDelay_Backoff(t *testing.T) {
	cfg := Config{InitialDelay: 2 * time.Second, MaxDelay: 20 * time.Second, BackoffFactor: 2, Jitter: false}

	d1 := nextDelay(cfg, 1) // before attempt 2
	d2 := nextDelay(cfg, 2) // before attempt 3
	d3 := nextDelay(cfg, 3) // before attempt 4

	if d1 != 2*time.Second {
		t.Errorf("d1 = %v, want 2s", d1)
	}
	if d2 != 4*time.Second {
		t.Errorf("d2 = %v, want 4s", d2)
	}
	if d3 != 8*time.Second {
		t.Errorf("d3 = %v, want 8s", d3)
	}
}

func TestNextDelay_MaxDelayCap(t *testing.T) {
	cfg := Config{InitialDelay: 10 * time.Second, MaxDelay: 12 * time.Second, BackoffFactor: 2, Jitter: false}
	d := nextDelay(cfg, 2) // 10*2 = 20s → capped at 12s
	if d != 12*time.Second {
		t.Errorf("d = %v, want 12s (capped)", d)
	}
}

func TestNextDelay_Jitter(t *testing.T) {
	cfg := Config{InitialDelay: 10 * time.Second, MaxDelay: 30 * time.Second, BackoffFactor: 1, Jitter: true}
	// With jitter the delay must be within ±25% of InitialDelay.
	seen := make(map[time.Duration]bool)
	for i := 0; i < 20; i++ {
		d := nextDelay(cfg, 1)
		low := 7500 * time.Millisecond
		high := 12500 * time.Millisecond
		if d < low || d > high {
			t.Errorf("jittered delay %v outside [%v, %v]", d, low, high)
		}
		seen[d] = true
	}
	// Very unlikely all 20 draws are identical if jitter is actually applied.
	if len(seen) < 3 {
		t.Errorf("jitter produced only %d distinct values — jitter may be disabled", len(seen))
	}
}

// Package retryutil provides a generic exponential-backoff re-check loop used
// by mutation tools to verify that an operation actually took effect before
// returning a failure to the LLM.
package retryutil

import (
	"context"
	"math/rand"
	"time"
)

// Config controls how WaitUntilResolved retries the check function.
type Config struct {
	MaxAttempts   int           // total check attempts (including the first one)
	InitialDelay  time.Duration // wait before attempt 2
	MaxDelay      time.Duration // upper cap on computed delay
	BackoffFactor float64       // multiplier applied to delay each round
	Jitter        bool          // add ±25% random noise to avoid thundering herds
}

// Default is the recommended config for post-mutation verification re-checks.
// Three attempts with 3 s / 6 s delays gives a ~15 s window before giving up.
var Default = Config{
	MaxAttempts:   3,
	InitialDelay:  3 * time.Second,
	MaxDelay:      15 * time.Second,
	BackoffFactor: 2.0,
	Jitter:        true,
}

// WaitUntilResolved polls check() until it returns (true, nil) or MaxAttempts
// is exhausted. Between attempts it sleeps for an exponentially growing delay
// (capped at MaxDelay, optionally jittered).
//
// afterAttempt, if non-nil, is called after every check with the 1-indexed
// attempt number and whether that check resolved. Use it to emit audit events
// without coupling this package to the audit subsystem.
//
// Returns:
//   - resolved: true if check() returned true before attempts were exhausted
//   - attempts: number of check() calls made (1 = resolved on first try)
//   - err: non-nil only if ctx was cancelled or check() returned an error
func WaitUntilResolved(
	ctx context.Context,
	cfg Config,
	check func() (resolved bool, err error),
	afterAttempt func(attempt int, resolved bool),
) (resolved bool, attempts int, err error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}

	for i := 1; i <= cfg.MaxAttempts; i++ {
		if err := ctx.Err(); err != nil {
			return false, i - 1, err
		}

		ok, checkErr := check()
		if checkErr != nil {
			if afterAttempt != nil {
				afterAttempt(i, false)
			}
			return false, i, checkErr
		}

		if afterAttempt != nil {
			afterAttempt(i, ok)
		}

		if ok {
			return true, i, nil
		}

		// Don't sleep after the last attempt.
		if i < cfg.MaxAttempts {
			delay := nextDelay(cfg, i)
			select {
			case <-ctx.Done():
				return false, i, ctx.Err()
			case <-time.After(delay):
			}
		}
	}

	return false, cfg.MaxAttempts, nil
}

// nextDelay computes the sleep duration before attempt number (attempt+1).
// attempt is 1-indexed (attempt=1 → delay before the 2nd check).
func nextDelay(cfg Config, attempt int) time.Duration {
	delay := float64(cfg.InitialDelay)
	for i := 1; i < attempt; i++ {
		delay *= cfg.BackoffFactor
	}
	if cfg.MaxDelay > 0 {
		if d := time.Duration(delay); d > cfg.MaxDelay {
			delay = float64(cfg.MaxDelay)
		}
	}
	if cfg.Jitter && delay > 0 {
		// ±25% uniform jitter.
		jitter := delay * 0.25
		delay += (rand.Float64()*2 - 1) * jitter //nolint:gosec // non-crypto jitter
	}
	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay)
}

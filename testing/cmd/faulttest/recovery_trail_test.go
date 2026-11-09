package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The execution stage is written by the remediation agent under the hop's own
// trace, which this CLI suffixes with -remed. Found live on a VM run: the check
// looked only under the fault's trace and the run ID, so it reported "no
// execution stage" for a restore whose execution stage was on record.
func TestCheckRecoveryTrail_QueriesTheRemediationHopsTrace(t *testing.T) {
	old1, old2 := recoveryTrailWait, recoveryTrailInterval
	recoveryTrailWait, recoveryTrailInterval = 10*time.Millisecond, 5*time.Millisecond
	defer func() { recoveryTrailWait, recoveryTrailInterval = old1, old2 }()

	var mu sync.Mutex
	asked := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.URL.Query().Get("trace_id"); id != "" {
			mu.Lock()
			asked[id] = true
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cfg := &HarnessConfig{}
	cfg.AuditURL = srv.URL
	res := EvalResult{Passed: true}
	checkRecoveryTrail(cfg, Failure{ID: "f"}, "faulttest-r-f", "plr_1", time.Now(), &res)

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"faulttest-r-f", "faulttest-r-f" + remedTraceSuffix, "plr_1"} {
		if !asked[want] {
			t.Errorf("the trail was never read under %q (asked: %v)", want, asked)
		}
	}
}

func TestCheckRecoveryTrail(t *testing.T) {
	f := Failure{ID: "db-pitr-dropped-table-vm"}

	t.Run("no audit URL: reported, not failed", func(t *testing.T) {
		res := EvalResult{Passed: true}
		checkRecoveryTrail(&HarnessConfig{}, f, "tr", "plr", time.Now(), &res)
		if !res.Passed || len(res.RecoveryTrailProblems) != 0 {
			t.Errorf("a run with no audit URL must not fail: %+v", res)
		}
	})

	t.Run("an empty trail fails the fault", func(t *testing.T) {
		old1, old2 := recoveryTrailWait, recoveryTrailInterval
		recoveryTrailWait, recoveryTrailInterval = 50*time.Millisecond, 10*time.Millisecond
		defer func() { recoveryTrailWait, recoveryTrailInterval = old1, old2 }()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v1/verify") {
				_, _ = w.Write([]byte(`{"valid":true}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		}))
		defer srv.Close()

		cfg := &HarnessConfig{}
		cfg.AuditURL = srv.URL
		res := EvalResult{Passed: true}
		checkRecoveryTrail(cfg, f, "tr", "plr", time.Now(), &res)
		if res.Passed || len(res.RecoveryTrailProblems) == 0 {
			t.Errorf("a trail with no recovery_decision events must fail the fault: %+v", res)
		}
	})
}

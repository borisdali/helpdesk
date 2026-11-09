package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

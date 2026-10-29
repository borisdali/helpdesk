package faultlib

import (
	"context"
	"strings"
	"testing"
	"time"
)

// verify_exec replaces verify_sql for a target the harness cannot open a
// psql connection to (a Kubernetes pod behind a ClusterIP Service).

func TestPollRecoveryExec_SucceedsWhenTheScriptExitsZero(t *testing.T) {
	secs, err := PollRecoveryExec(context.Background(), "exit 0", "", 10*time.Second)
	if err != nil {
		t.Fatalf("PollRecoveryExec: %v", err)
	}
	if secs < 0 || secs > 5 {
		t.Errorf("elapsed = %.2fs, want an immediate success", secs)
	}
}

func TestPollRecoveryExec_PassesTheKubeContext(t *testing.T) {
	script := `[ "$FAULTTEST_K8S_CONTEXT" = "minikube" ]`
	if _, err := PollRecoveryExec(context.Background(), script, "minikube", 10*time.Second); err != nil {
		t.Errorf("the script did not see FAULTTEST_K8S_CONTEXT=minikube: %v", err)
	}
	// And no context set means the variable is not invented.
	script = `[ -z "$FAULTTEST_K8S_CONTEXT_UNSET_BY_TEST" ]`
	if _, err := PollRecoveryExec(context.Background(), script, "", 10*time.Second); err != nil {
		t.Errorf("unexpected failure: %v", err)
	}
}

// A script that never succeeds is retried until the context ends and is
// never reported as recovered.
func TestPollRecoveryExec_NeverSucceedingScriptIsNotRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := PollRecoveryExec(ctx, `echo "pod not ready yet" >&2; exit 3`, "", time.Minute); err == nil {
		t.Fatal("a script that never succeeds must not report recovery")
	}
}

// VerifyRecovery uses verify_exec instead of psql when the fault declares it:
// with a ConnStr no psql could open, the exec script alone decides.
func TestVerifyRecovery_UsesVerifyExecInsteadOfSQL(t *testing.T) {
	r := NewRemediator(&HarnessConfig{ConnStr: "host=does-not-exist.invalid port=1"})
	f := Failure{
		ID:      "db-pgdata-corrupted-k8s",
		Timeout: "300s",
		Remediation: RemediationSpec{
			VerifyExec:    "exit 0",
			VerifyTimeout: "10s",
		},
	}
	res := r.VerifyRecovery(context.Background(), f, "playbook")
	if res.Err != nil || !res.Passed {
		t.Fatalf("VerifyRecovery with verify_exec = %+v, want a pass without any SQL connection", res)
	}
	if res.Score <= 0 {
		t.Errorf("Score = %.2f, want the speed score", res.Score)
	}
}

func TestVerifyRecovery_VerifyExecFailureNamesTheCheck(t *testing.T) {
	r := NewRemediator(&HarnessConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	f := Failure{Remediation: RemediationSpec{VerifyExec: "exit 1", VerifyTimeout: "1s"}}
	res := r.VerifyRecovery(ctx, f, "playbook")
	if res.Passed || res.Err == nil || !strings.Contains(res.Err.Error(), "recovery verification") {
		t.Errorf("VerifyRecovery = %+v, want a recovery verification error", res)
	}
}

package faultlib

import (
	"context"
	"path/filepath"
	"testing"
)

// TestResolvedContainerName_MatchesRawDSNAgainstInfraConfig is a regression
// test for the db-connection-refused failure seen under `go test -tags
// faulttest ./testing/faulttest/...`: this package's execShell never set
// $FAULTTEST_CONTAINER at all (unlike testing/cmd/faulttest's injector,
// which was already fixed to resolve it via FindDBByConnStr), so
// db-connection-refused's "docker stop $FAULTTEST_CONTAINER" failed with
// "invalid container name or ID: value is empty". Real invocations (e.g.
// `make faulttest-gateway`) set FAULTTEST_CONN_STR to the raw DSN, not the
// infra config alias, so lookup must match by connection string too.
func TestResolvedContainerName_MatchesRawDSNAgainstInfraConfig(t *testing.T) {
	testingDir := findTestingDirForTest(t)
	cfg := &HarnessConfig{
		ConnStr:         "host=localhost port=15432 dbname=testdb user=postgres password=testpass",
		InfraConfigPath: filepath.Join(testingDir, "testing.infra.json"),
	}
	inj := NewInjector(cfg)

	got := inj.resolvedContainerName()
	if got != "helpdesk-test-pg" {
		t.Errorf("resolvedContainerName() = %q, want %q (raw DSN must resolve via FindDBByConnStr against testing.infra.json's faulttest-db entry)", got, "helpdesk-test-pg")
	}
}

// TestResolvedConnEnv_MatchesRawDSNAgainstInfraConfig locks in the same fix
// for resolvedConnEnv: a raw DSN matching an infra config entry's
// connection_string must resolve to that entry (and its password_env), not
// silently fall through as if no infra config were configured.
func TestResolvedConnEnv_MatchesRawDSNAgainstInfraConfig(t *testing.T) {
	testingDir := findTestingDirForTest(t)
	rawDSN := "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
	cfg := &HarnessConfig{
		ConnStr:         rawDSN,
		InfraConfigPath: filepath.Join(testingDir, "testing.infra.json"),
	}
	inj := NewInjector(cfg)

	gotConn, _ := inj.resolvedConnEnv()
	if gotConn == "" {
		t.Fatal("resolvedConnEnv() returned empty connection string")
	}
}

// TestResolvedContainerName_NoMatchReturnsEmpty verifies the function returns
// "" (rather than panicking or guessing) when the DSN has no infra config
// match, since this package has no auto-db fallback.
func TestResolvedContainerName_NoMatchReturnsEmpty(t *testing.T) {
	cfg := &HarnessConfig{
		ConnStr:         "host=unknown port=9999 dbname=nope user=nobody",
		InfraConfigPath: "",
	}
	inj := NewInjector(cfg)

	if got := inj.resolvedContainerName(); got != "" {
		t.Errorf("resolvedContainerName() = %q, want empty string with no infra config", got)
	}
}

// TestExecConfig_TeardownDoesNotRestore_CallerMustResetConnStr verifies the
// assumption RunFaultCycle/TeardownFault's callers rely on (Part B, v0.26):
// execConfig-type faults (db-auth-failure, db-not-exist) mutate cfg.ConnStr
// on Inject, and their teardown spec is {type: config, restore: true} — but
// execConfig never reads the Restore field at all, so Teardown alone does
// NOT put ConnStr back. The actual restoration only happens because callers
// explicitly reset cfg.ConnStr themselves before calling Teardown (the
// beforeTeardown callback on the automatic/injection-failure path,
// RunFaultCycle callers' own origConn-reset line on the success path). If
// this test's first assertion (post-Teardown-alone) ever starts failing,
// execConfig gained real restore behavior and the caller-side reset dance
// may have become redundant defensive code rather than load-bearing.
func TestExecConfig_TeardownDoesNotRestore_CallerMustResetConnStr(t *testing.T) {
	origConn := "host=good port=5432 dbname=testdb"
	cfg := &HarnessConfig{ConnStr: origConn}
	inj := NewInjector(cfg)
	f := Failure{
		Inject: InjectSpec{
			Type:     "config",
			Override: map[string]string{"connection_string": "host=bad port=5432 dbname=testdb password=wrong"},
		},
		Teardown: InjectSpec{Type: "config", Restore: true},
	}

	if err := inj.Inject(context.Background(), f); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if cfg.ConnStr == origConn {
		t.Fatal("Inject should have mutated cfg.ConnStr to the override value")
	}
	mutated := cfg.ConnStr

	if err := inj.Teardown(context.Background(), f); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if cfg.ConnStr != mutated {
		t.Fatalf("Teardown alone changed cfg.ConnStr from %q to %q — execConfig now reads Restore; "+
			"see this test's doc comment", mutated, cfg.ConnStr)
	}

	// Now the actual pattern callers use: reset BEFORE calling Teardown.
	cfg.ConnStr = origConn
	if err := inj.Teardown(context.Background(), f); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if cfg.ConnStr != origConn {
		t.Errorf("cfg.ConnStr = %q after caller-side reset + Teardown, want %q", cfg.ConnStr, origConn)
	}
}

// findTestingDirForTest locates the testing/ directory the same way
// loadConfigFromEnv's production equivalent (testing/faulttest) does, so
// this test works regardless of the working directory `go test` is invoked
// from.
func findTestingDirForTest(t *testing.T) string {
	t.Helper()
	// From testing/faultlib/, testing/ is one level up.
	dir, err := filepath.Abs("../")
	if err != nil {
		t.Fatalf("resolve testing dir: %v", err)
	}
	return dir
}

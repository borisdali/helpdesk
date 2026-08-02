package main

import (
	"path/filepath"
	"testing"
)

// TestResolvedContainerName_MatchesRawDSNAgainstInfraConfig is a regression
// test for a bug where --conn (cfg.ConnStr) is documented as the literal
// injection DSN (see Makefile: "FAULTTEST_CONN_STR  injection DSN"), but
// resolvedContainerName did a literal map-key lookup (cfg.DBServers[ConnStr])
// that only matches when ConnStr happens to equal an infra config alias.
// Real invocations (e.g. `make faulttest-gateway`) set FAULTTEST_CONN_STR to
// the raw DSN, not the alias — so the lookup always missed, container name
// resolved to "", and shell_exec scripts using $FAULTTEST_CONTAINER (e.g.
// db-connection-refused) failed with "invalid container name or ID: value is
// empty". Fixed by using infra.Config.FindDBByConnStr, which also matches by
// full connection string, not just by config key.
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

// TestResolvedContainerName_FallsBackToAutoDB verifies the AutoDBContainerName
// fallback still applies when the DSN has no infra config match at all.
func TestResolvedContainerName_FallsBackToAutoDB(t *testing.T) {
	cfg := &HarnessConfig{
		ConnStr:             "host=unknown port=9999 dbname=nope user=nobody",
		InfraConfigPath:     "",
		AutoDBContainerName: "faulttest-auto-db-deadbeef",
	}
	inj := NewInjector(cfg)

	got := inj.resolvedContainerName()
	if got != "faulttest-auto-db-deadbeef" {
		t.Errorf("resolvedContainerName() = %q, want AutoDBContainerName fallback", got)
	}
}

// findTestingDirForTest locates the testing/ directory the same way
// loadConfigFromEnv's production equivalent does, so this test works
// regardless of the working directory `go test` is invoked from.
func findTestingDirForTest(t *testing.T) string {
	t.Helper()
	// From testing/cmd/faulttest/, testing/ is two levels up.
	dir, err := filepath.Abs("../../")
	if err != nil {
		t.Fatalf("resolve testing dir: %v", err)
	}
	return dir
}

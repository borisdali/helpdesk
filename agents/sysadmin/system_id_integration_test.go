//go:build integration

// Integration test for the source system identifier read against the real
// pgBackRest test container (see restore_from_backup_integration_test.go for the
// fixture). Read-only.
//
//	go test -tags integration -timeout 120s ./agents/sysadmin/... -run SystemID
package main

import (
	"context"
	"path/filepath"
	"testing"

	"helpdesk/internal/infra"
)

func TestIntegration_SystemID_BothWaysAgreeWithTheServer(t *testing.T) {
	skipIfNoPgBackRestDocker(t)
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })

	want := fixturePSQL(t, "SELECT system_identifier FROM pg_control_system()")
	if want == "" {
		t.Fatal("the fixture reports no system identifier")
	}
	ctx := context.Background()

	// The running instance is asked.
	if got := readSourceSystemID(ctx, "pgbackrest-db"); got != want {
		t.Errorf("asked the running instance: %q, want %q", got, want)
	}

	// With the server unreachable the identifier is read off the data directory
	// instead, as it is for an instance that is down. The same number.
	inst, err := instanceFor("pgbackrest-db")
	if err != nil {
		t.Fatal(err)
	}
	out, err := inst.maintenance(ctx, "export PGPORT=1\n"+systemIDScript, false)
	if err != nil {
		t.Fatalf("pg_controldata path: %v\n%s", err, out)
	}
	if got := parseSystemID(out); got != want {
		t.Errorf("read off the data directory: %q, want %q (%s)", got, want, out)
	}

	// An unknown target reads nothing, and is not an error.
	if got := readSourceSystemID(ctx, "no-such-database"); got != "" {
		t.Errorf("an unknown target gave %q", got)
	}
}

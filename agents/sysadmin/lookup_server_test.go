package main

import (
	"strings"
	"testing"

	"helpdesk/internal/infra"
)

// lookupServer delegates to infra.ResolveTarget (which the gateway's incident
// ledger shares). These pin what the agents see: the entry returned, and the
// exact refusals, so the delegation cannot change how a target is read.
func lookupFixture() *infra.Config {
	return &infra.Config{DBServers: map[string]infra.DBServer{
		"prod-main":  {Name: "Prod Main", ConnectionString: "host=db1.example.com port=5432 dbname=app", VMName: "vm1"},
		"prod-other": {Name: "Prod Other", ConnectionString: "host=db2.example.com port=5432 dbname=app"},
		"docker-pg":  {ConnectionString: "host=localhost port=15432", ContainerName: "pg-fixture"},
		"cont-a":     {ConnectionString: "host=ca port=7000", ContainerName: "shared-name"},
		"cont-b":     {ConnectionString: "host=cb port=7001", ContainerName: "shared-name"},
	}}
}

func TestLookupServer_ResolvesAndReturnsTheEntry(t *testing.T) {
	ic := lookupFixture()
	for _, tc := range []struct{ name, target, wantKey string }{
		{"the server ID", "prod-main", "prod-main"},
		{"the server ID with surrounding space", " prod-main\n", "prod-main"},
		{"a connection string", "host=db2.example.com port=5432 dbname=app user=u password=p", "prod-other"},
		{"a bare port only one server has", "15432", "docker-pg"},
		{"a container name", "pg-fixture", "docker-pg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, db, err := lookupServer(ic, tc.target)
			if err != nil || key != tc.wantKey {
				t.Fatalf("lookupServer(%q) = %q, %v; want %q", tc.target, key, err, tc.wantKey)
			}
			if db.ConnectionString != ic.DBServers[tc.wantKey].ConnectionString {
				t.Errorf("returned the wrong entry: %+v", db)
			}
		})
	}
	if _, db, _ := lookupServer(ic, "prod-main"); db.VMName != "vm1" {
		t.Errorf("the entry's own fields were lost: %+v", db)
	}
}

func TestLookupServer_RefusesToGuess(t *testing.T) {
	ic := lookupFixture()
	// Port 5432 is on two servers: refuse, naming both, rather than pick one.
	_, _, err := lookupServer(ic, "5432")
	if err == nil || !strings.Contains(err.Error(), "refusing to guess") ||
		!strings.Contains(err.Error(), "prod-main, prod-other") || !strings.Contains(err.Error(), "host+port") {
		t.Errorf("a shared bare port: %v", err)
	}
	// Two containers with one name: the agent must not act on whichever came first.
	_, _, err = lookupServer(ic, "shared-name")
	if err == nil || !strings.Contains(err.Error(), "refusing to guess") || !strings.Contains(err.Error(), "cont-a, cont-b") {
		t.Errorf("a shared container name: %v", err)
	}
	// The same refusals every time, whatever Go's map order.
	for i := 0; i < 100; i++ {
		if _, _, err := lookupServer(ic, "5432"); err == nil || !strings.Contains(err.Error(), "prod-main, prod-other") {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

func TestLookupServer_NotFoundListsTheKnownServers(t *testing.T) {
	ic := lookupFixture()
	for _, target := range []string{"nope", "host=elsewhere port=1", ""} {
		_, _, err := lookupServer(ic, target)
		if err == nil || !strings.Contains(err.Error(), "not found in infrastructure config") ||
			!strings.Contains(err.Error(), "Known servers: cont-a, cont-b, docker-pg, prod-main, prod-other") {
			t.Errorf("lookupServer(%q): %v", target, err)
		}
	}
}

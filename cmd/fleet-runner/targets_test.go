package main

import (
	"testing"

	"helpdesk/internal/fleet"
	"helpdesk/internal/infra"
)

func makeInfra() *infra.Config {
	return &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db-1": {Name: "prod-db-1", Tags: []string{"production", "primary"}},
			"prod-db-2": {Name: "prod-db-2", Tags: []string{"production", "replica"}},
			"stage-db":  {Name: "stage-db", Tags: []string{"staging"}},
			"dev-db":    {Name: "dev-db", Tags: []string{"dev"}},
		},
	}
}

func TestResolveTargets_ByTag(t *testing.T) {
	cfg := makeInfra()
	targets := fleet.Targets{Tags: []string{"production"}}
	servers, err := resolveTargets(cfg, targets)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(servers) != 2 {
		t.Errorf("expected 2 production servers, got %d: %v", len(servers), servers)
	}
}

func TestResolveTargets_ByName(t *testing.T) {
	cfg := makeInfra()
	targets := fleet.Targets{Names: []string{"stage-db", "dev-db"}}
	servers, err := resolveTargets(cfg, targets)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d: %v", len(servers), servers)
	}
}

func TestResolveTargets_Exclude(t *testing.T) {
	cfg := makeInfra()
	targets := fleet.Targets{Tags: []string{"production"}, Exclude: []string{"prod-db-2"}}
	servers, err := resolveTargets(cfg, targets)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(servers) != 1 {
		t.Errorf("expected 1 server after exclude, got %d: %v", len(servers), servers)
	}
	if servers[0] != "prod-db-1" {
		t.Errorf("expected prod-db-1, got %q", servers[0])
	}
}

func TestResolveTargets_All(t *testing.T) {
	cfg := makeInfra()
	targets := fleet.Targets{} // no filters — selects all
	servers, err := resolveTargets(cfg, targets)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(servers) != 4 {
		t.Errorf("expected 4 servers (all), got %d", len(servers))
	}
}

func TestResolveTargets_NilInfra(t *testing.T) {
	_, err := resolveTargets(nil, fleet.Targets{})
	if err == nil {
		t.Fatal("expected error for nil infra config")
	}
}

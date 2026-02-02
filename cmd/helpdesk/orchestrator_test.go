package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- loadInfraConfig tests ---

func TestLoadInfraConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "infra.json")
	data := `{
		"db_servers": {
			"prod-db": {
				"name": "Production DB",
				"connection_string": "host=db.example.com port=5432 dbname=prod",
				"k8s_cluster": "prod-cluster",
				"k8s_namespace": "database"
			}
		},
		"k8s_clusters": {
			"prod-cluster": {
				"name": "Production Cluster",
				"context": "gke_prod"
			}
		},
		"vms": {
			"vm-1": {
				"name": "DB Host VM",
				"host": "10.0.0.5"
			}
		}
	}`
	os.WriteFile(path, []byte(data), 0644)

	config, err := loadInfraConfig(path)
	if err != nil {
		t.Fatalf("loadInfraConfig error: %v", err)
	}
	if len(config.DBServers) != 1 {
		t.Errorf("DBServers count = %d, want 1", len(config.DBServers))
	}
	db := config.DBServers["prod-db"]
	if db.Name != "Production DB" {
		t.Errorf("db.Name = %q, want %q", db.Name, "Production DB")
	}
	if db.K8sCluster != "prod-cluster" {
		t.Errorf("db.K8sCluster = %q, want %q", db.K8sCluster, "prod-cluster")
	}
	if len(config.K8sClusters) != 1 {
		t.Errorf("K8sClusters count = %d, want 1", len(config.K8sClusters))
	}
	if len(config.VMs) != 1 {
		t.Errorf("VMs count = %d, want 1", len(config.VMs))
	}
}

func TestLoadInfraConfig_MissingFile(t *testing.T) {
	_, err := loadInfraConfig("/nonexistent/path/infra.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInfraConfig_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("{bad json"), 0644)

	_, err := loadInfraConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// --- loadAgentsConfig tests ---

func TestLoadAgentsConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `[
		{
			"name": "database_agent",
			"url": "http://localhost:1100",
			"description": "Database agent",
			"use_cases": ["check connections", "run queries"]
		},
		{
			"name": "k8s_agent",
			"url": "http://localhost:1102",
			"description": "Kubernetes agent"
		}
	]`
	os.WriteFile(path, []byte(data), 0644)

	agents, err := loadAgentsConfig(path)
	if err != nil {
		t.Fatalf("loadAgentsConfig error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("agents count = %d, want 2", len(agents))
	}
	if agents[0].Name != "database_agent" {
		t.Errorf("agents[0].Name = %q, want %q", agents[0].Name, "database_agent")
	}
	if len(agents[0].UseCases) != 2 {
		t.Errorf("agents[0].UseCases = %v, want 2 items", agents[0].UseCases)
	}
}

func TestLoadAgentsConfig_EmptyArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	os.WriteFile(path, []byte("[]"), 0644)

	agents, err := loadAgentsConfig(path)
	if err != nil {
		t.Fatalf("loadAgentsConfig error: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("agents count = %d, want 0", len(agents))
	}
}

func TestLoadAgentsConfig_MissingFile(t *testing.T) {
	_, err := loadAgentsConfig("/nonexistent/agents.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// --- buildInfraPromptSection tests ---

func TestBuildInfraPromptSection_Full(t *testing.T) {
	config := &InfraConfig{
		DBServers: map[string]DBServer{
			"prod-db": {
				Name:             "Production DB",
				ConnectionString: "host=db.example.com",
				K8sCluster:       "prod-cluster",
				K8sNamespace:     "database",
			},
		},
		K8sClusters: map[string]K8sCluster{
			"prod-cluster": {Name: "Prod Cluster", Context: "gke_prod"},
		},
		VMs: map[string]VM{
			"vm-1": {Name: "DB Host", Host: "10.0.0.5"},
		},
	}

	result := buildInfraPromptSection(config)

	if !strings.Contains(result, "Managed Infrastructure") {
		t.Error("missing 'Managed Infrastructure' header")
	}
	if !strings.Contains(result, "Database Servers") {
		t.Error("missing 'Database Servers' section")
	}
	if !strings.Contains(result, "prod-db") {
		t.Error("missing db server ID")
	}
	if !strings.Contains(result, "host=db.example.com") {
		t.Error("missing connection string")
	}
	if !strings.Contains(result, "gke_prod") {
		t.Error("missing k8s context")
	}
	if !strings.Contains(result, "Kubernetes Clusters") {
		t.Error("missing K8s section")
	}
	if !strings.Contains(result, "Virtual Machines") {
		t.Error("missing VMs section")
	}
	if !strings.Contains(result, "10.0.0.5") {
		t.Error("missing VM host")
	}
}

func TestBuildInfraPromptSection_Empty(t *testing.T) {
	config := &InfraConfig{}
	result := buildInfraPromptSection(config)

	if !strings.Contains(result, "Managed Infrastructure") {
		t.Error("missing header even for empty config")
	}
	if strings.Contains(result, "Database Servers") {
		t.Error("should not have DB section for empty config")
	}
}

func TestBuildInfraPromptSection_DBOnVM(t *testing.T) {
	config := &InfraConfig{
		DBServers: map[string]DBServer{
			"vm-db": {
				Name:             "VM Database",
				ConnectionString: "host=10.0.0.5",
				VMName:           "vm-1",
			},
		},
		VMs: map[string]VM{
			"vm-1": {Name: "DB Host", Host: "10.0.0.5"},
		},
	}

	result := buildInfraPromptSection(config)
	if !strings.Contains(result, "Runs on VM") {
		t.Error("missing VM reference for DB")
	}
}

// --- buildAgentPromptSection tests ---

func TestBuildAgentPromptSection_Multiple(t *testing.T) {
	agents := []AgentConfig{
		{
			Name:        "database_agent",
			Description: "Manages PostgreSQL databases",
			UseCases:    []string{"Check connectivity", "Run diagnostics"},
		},
		{
			Name:        "k8s_agent",
			Description: "Manages Kubernetes clusters",
		},
	}

	result := buildAgentPromptSection(agents)

	if !strings.Contains(result, "Available Specialist Agents") {
		t.Error("missing header")
	}
	if !strings.Contains(result, "database_agent") {
		t.Error("missing database_agent")
	}
	if !strings.Contains(result, "k8s_agent") {
		t.Error("missing k8s_agent")
	}
	if !strings.Contains(result, "Check connectivity") {
		t.Error("missing use case")
	}
}

func TestBuildAgentPromptSection_Empty(t *testing.T) {
	result := buildAgentPromptSection(nil)
	if result != "" {
		t.Errorf("expected empty string for nil agents, got %q", result)
	}
}

func TestBuildAgentPromptSection_Single(t *testing.T) {
	agents := []AgentConfig{
		{Name: "incident_agent", Description: "Creates incident bundles"},
	}

	result := buildAgentPromptSection(agents)
	if !strings.Contains(result, "incident_agent") {
		t.Error("missing agent name")
	}
	if !strings.Contains(result, "Creates incident bundles") {
		t.Error("missing description")
	}
}

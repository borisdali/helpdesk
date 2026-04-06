// Package infra provides infrastructure configuration types and loading.
package infra

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// DBServer represents a managed database server (AlloyDB Omni, standalone PostgreSQL, etc.).
type DBServer struct {
	Name             string   `json:"name"`
	ConnectionString string   `json:"connection_string"`
	K8sCluster       string   `json:"k8s_cluster,omitempty"`
	K8sNamespace     string   `json:"k8s_namespace,omitempty"`
	K8sPodSelector   string   `json:"k8s_pod_selector,omitempty"` // label selector for kubectl exec (e.g. "app=postgres,instance=prod")
	VMName           string   `json:"vm_name,omitempty"`
	ContainerName    string   `json:"container_name,omitempty"` // container name when VM.Runtime is docker|podman
	SystemdUnit      string   `json:"systemd_unit,omitempty"`   // unit name when VM.Runtime is ""
	Tags             []string `json:"tags,omitempty"`           // Tags for policy matching (e.g., "production", "staging")
	Sensitivity      []string `json:"sensitivity,omitempty"`    // Sensitivity classes (e.g., "pii", "critical")
}

// K8sCluster represents a managed Kubernetes cluster.
type K8sCluster struct {
	Name        string   `json:"name"`
	Context     string   `json:"context"`
	Tags        []string `json:"tags,omitempty"`        // Tags for policy matching (e.g., "production", "staging")
	Sensitivity []string `json:"sensitivity,omitempty"` // Sensitivity classes (e.g., "critical")
}

// VM represents a physical or virtual machine hosting one or more database services.
// It is the operational unit for the sysadmin agent.
type VM struct {
	Name    string `json:"name"`
	Address string `json:"address"`          // hostname or IP address
	Runtime string `json:"runtime,omitempty"` // container runtime: "docker", "podman", or "" (systemd/direct)
}

// Config holds the infrastructure inventory.
type Config struct {
	DBServers   map[string]DBServer   `json:"db_servers"`
	K8sClusters map[string]K8sCluster `json:"k8s_clusters"`
	VMs         map[string]VM         `json:"vms"`
}

// Load loads infrastructure configuration from a JSON file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read infrastructure config file: %v", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse infrastructure config: %v", err)
	}

	return &config, nil
}

// DBInfo returns a formatted description of a database server with its hosting info expanded.
type DBInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ConnectionString string `json:"connection_string"`
	Hosting          string `json:"hosting"`
	K8sContext       string `json:"k8s_context,omitempty"`
	K8sNamespace     string `json:"k8s_namespace,omitempty"`
	VMAddress        string `json:"vm_address,omitempty"`
	VMRuntime        string `json:"vm_runtime,omitempty"`
	ContainerName    string `json:"container_name,omitempty"`
	SystemdUnit      string `json:"systemd_unit,omitempty"`
}

// ListDatabases returns a list of all database servers with expanded hosting info.
func (c *Config) ListDatabases() []DBInfo {
	if c == nil || len(c.DBServers) == 0 {
		return nil
	}

	var dbs []DBInfo
	for id, db := range c.DBServers {
		info := DBInfo{
			ID:               id,
			Name:             db.Name,
			ConnectionString: db.ConnectionString,
			ContainerName:    db.ContainerName,
			SystemdUnit:      db.SystemdUnit,
		}

		if db.K8sCluster != "" {
			if k8s, ok := c.K8sClusters[db.K8sCluster]; ok {
				info.Hosting = fmt.Sprintf("Kubernetes: %s", k8s.Name)
				info.K8sContext = k8s.Context
				info.K8sNamespace = db.K8sNamespace
				if info.K8sNamespace == "" {
					info.K8sNamespace = "default"
				}
			} else {
				info.Hosting = fmt.Sprintf("Kubernetes: %s (not configured)", db.K8sCluster)
			}
		} else if db.VMName != "" {
			if vm, ok := c.VMs[db.VMName]; ok {
				info.Hosting = fmt.Sprintf("VM: %s", vm.Name)
				info.VMAddress = vm.Address
				info.VMRuntime = vm.Runtime
			} else {
				info.Hosting = fmt.Sprintf("VM: %s (not configured)", db.VMName)
			}
		} else {
			info.Hosting = "Standalone"
		}

		dbs = append(dbs, info)
	}

	return dbs
}

// Summary returns a human-readable summary of the infrastructure.
func (c *Config) Summary() string {
	if c == nil {
		return "No infrastructure configured."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Infrastructure: %d database(s), %d K8s cluster(s), %d VM(s)\n",
		len(c.DBServers), len(c.K8sClusters), len(c.VMs)))

	if len(c.DBServers) > 0 {
		sb.WriteString("\nDatabases:\n")
		for _, db := range c.ListDatabases() {
			line := fmt.Sprintf("  - %s (%s): %s", db.ID, db.Name, db.Hosting)
			switch {
			case db.VMRuntime != "" && db.ContainerName != "":
				line += fmt.Sprintf(" [%s container: %s]", db.VMRuntime, db.ContainerName)
			case db.SystemdUnit != "":
				line += fmt.Sprintf(" [systemd: %s]", db.SystemdUnit)
			}
			sb.WriteString(line + "\n")
		}
	}

	return sb.String()
}

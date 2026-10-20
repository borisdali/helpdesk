package main

import (
	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/toolregistry"
)

// cardOptions returns the agent card metadata for the Kubernetes agent's tools.
// Every tool must declare at least one capability; tools_reference_test.go
// enforces this and keeps docs/tools/k8s.md in sync.
func cardOptions(tools []tool.Tool) agentutil.CardOptions {
	return agentutil.CardOptions{
		Version:  buildinfo.Version,
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"k8s_agent":                            {"kubernetes", "infrastructure", "diagnostics"},
			"k8s_agent-get_pods":                   {"kubernetes", "pods", "workloads"},
			"k8s_agent-get_service":                {"kubernetes", "services", "networking"},
			"k8s_agent-describe_service":           {"kubernetes", "services", "networking"},
			"k8s_agent-get_endpoints":              {"kubernetes", "endpoints", "networking"},
			"k8s_agent-get_events":                 {"kubernetes", "events", "cluster"},
			"k8s_agent-get_pod_logs":               {"kubernetes", "logs", "debugging"},
			"k8s_agent-read_pod_file":              {"kubernetes", "logs", "debugging"},
			"k8s_agent-describe_pod":               {"kubernetes", "pods", "debugging"},
			"k8s_agent-get_nodes":                  {"kubernetes", "nodes", "cluster"},
			"k8s_agent-delete_pod":                 {"kubernetes", "pods", "remediation"},
			"k8s_agent-restart_deployment":         {"kubernetes", "deployments", "remediation"},
			"k8s_agent-scale_deployment":           {"kubernetes", "deployments", "remediation"},
			"k8s_agent-debug_node_dmesg":           {"kubernetes", "nodes", "diagnostics"},
			"k8s_agent-patch_deployment_resources": {"kubernetes", "deployments", "remediation"},
		},
		SkillExamples: map[string][]string{
			"k8s_agent-get_pods":      {"List all pods in the database namespace"},
			"k8s_agent-get_events":    {"Show recent warning events in the cluster"},
			"k8s_agent-get_pod_logs":  {"Get the last 100 lines of logs from the postgres pod"},
			"k8s_agent-get_endpoints": {"Check if the database service has healthy endpoints"},
		},
		SkillSchemaHash: agentutil.ComputeSchemaFingerprints("k8s_agent", tools),
		ToolSchemas:     agentutil.ComputeInputSchemas(tools),
		SkillCapabilities: map[string][]string{
			"k8s_agent-get_pods":                   {toolregistry.CapPodList},
			"k8s_agent-get_service":                {toolregistry.CapServiceInfo},
			"k8s_agent-describe_service":           {toolregistry.CapServiceInfo},
			"k8s_agent-get_endpoints":              {toolregistry.CapServiceInfo},
			"k8s_agent-get_events":                 {toolregistry.CapEventList},
			"k8s_agent-get_pod_logs":               {toolregistry.CapLogs},
			"k8s_agent-read_pod_file":              {toolregistry.CapLogs},
			"k8s_agent-describe_pod":               {toolregistry.CapPodDetail},
			"k8s_agent-get_nodes":                  {toolregistry.CapNodeList},
			"k8s_agent-get_node_status":            {toolregistry.CapNodeHealth},
			"k8s_agent-get_pod_resources":          {toolregistry.CapResourceUsage},
			"k8s_agent-debug_node_dmesg":           {toolregistry.CapKernelLog, toolregistry.CapDebugPodCreate},
			"k8s_agent-delete_pod":                 {toolregistry.CapWorkloadRestart},
			"k8s_agent-restart_deployment":         {toolregistry.CapWorkloadRestart},
			"k8s_agent-scale_deployment":           {toolregistry.CapDeploymentScale},
			"k8s_agent-patch_deployment_resources": {toolregistry.CapResourceLimitsChange, toolregistry.CapWorkloadRestart},
		},
	}
}

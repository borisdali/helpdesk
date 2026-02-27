// Package main implements the Kubernetes troubleshooting agent.
// It exposes kubectl-based tools via the A2A protocol for diagnosing
// infrastructure issues in Kubernetes environments.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/google/uuid"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/prompts"
)

// infraConfig holds the loaded infrastructure configuration (if available).
// Used to resolve database names to K8s namespace/context.
var infraConfig *infra.Config

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1102")
	ctx := context.Background()

	// Enforce governance compliance in fix mode before any other initialization.
	agentutil.EnforceFixMode(ctx, agentutil.CheckFixModeViolations(cfg), "k8s_agent", cfg.AuditURL)

	// Load infrastructure config if available (enables database name resolution)
	if infraPath := os.Getenv("HELPDESK_INFRA_CONFIG"); infraPath != "" {
		var err error
		infraConfig, err = infra.Load(infraPath)
		if err != nil {
			slog.Warn("failed to load infrastructure config", "path", infraPath, "err", err)
		} else {
			slog.Info("infrastructure config loaded", "databases", len(infraConfig.DBServers))
		}
	}

	// Initialize audit store if enabled
	auditStore, err := agentutil.InitAuditStore(cfg)
	if err != nil {
		slog.Error("failed to initialize audit store", "err", err)
		os.Exit(1)
	}

	// Create trace store for propagating trace_id from incoming requests
	traceStore := &audit.CurrentTraceStore{}

	if auditStore != nil {
		defer auditStore.Close()
		// Create tool auditor with trace store for dynamic trace_id
		sessionID := "k8sagent_" + uuid.New().String()[:8]
		toolAuditor = audit.NewToolAuditorWithTraceStore(auditStore, "k8s_agent", sessionID, traceStore)
		slog.Info("tool auditing enabled", "session_id", sessionID)
	}

	// Initialize policy engine if configured
	policyEngine, err := agentutil.InitPolicyEngine(cfg)
	if err != nil {
		slog.Error("failed to initialize policy engine", "err", err)
		os.Exit(1)
	}
	policyEnforcer = agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{
		Engine:         policyEngine,
		PolicyCheckURL: cfg.PolicyCheckURL,
		TraceStore:     traceStore,
		AgentName:      "k8s_agent",
		ToolAuditor:    toolAuditor,
	})

	slog.Info("governance",
		"audit", auditStore != nil,
		"policy", policyEngine != nil,
	)

	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create LLM model", "err", err)
		os.Exit(1)
	}

	tools, err := createTools()
	if err != nil {
		slog.Error("failed to create tools", "err", err)
		os.Exit(1)
	}

	instruction := prompts.K8s
	if infraConfig != nil {
		instruction += "\n\n## Known Infrastructure\n\n" + infraConfig.Summary()
	}

	k8sAgent, err := llmagent.New(llmagent.Config{
		Name:        "k8s_agent",
		Description: "Kubernetes troubleshooting agent that can inspect pods, services, endpoints, events, and logs to diagnose infrastructure issues.",
		Instruction: instruction,
		Model:       llmModel,
		Tools:       tools,
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			agentutil.NewReasoningCallback(toolAuditor),
		},
	})
	if err != nil {
		slog.Error("failed to create k8s agent", "err", err)
		os.Exit(1)
	}

	cardOpts := agentutil.CardOptions{
		Version:  "1.0.0",
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"k8s_agent":                       {"kubernetes", "infrastructure", "diagnostics"},
			"k8s_agent-get_pods":              {"kubernetes", "pods", "workloads"},
			"k8s_agent-get_service":           {"kubernetes", "services", "networking"},
			"k8s_agent-describe_service":      {"kubernetes", "services", "networking"},
			"k8s_agent-get_endpoints":         {"kubernetes", "endpoints", "networking"},
			"k8s_agent-get_events":            {"kubernetes", "events", "cluster"},
			"k8s_agent-get_pod_logs":          {"kubernetes", "logs", "debugging"},
			"k8s_agent-describe_pod":          {"kubernetes", "pods", "debugging"},
			"k8s_agent-get_nodes":             {"kubernetes", "nodes", "cluster"},
			"k8s_agent-delete_pod":            {"kubernetes", "pods", "remediation"},
			"k8s_agent-restart_deployment":    {"kubernetes", "deployments", "remediation"},
			"k8s_agent-scale_deployment":      {"kubernetes", "deployments", "remediation"},
		},
		SkillExamples: map[string][]string{
			"k8s_agent-get_pods":      {"List all pods in the database namespace"},
			"k8s_agent-get_events":    {"Show recent warning events in the cluster"},
			"k8s_agent-get_pod_logs":  {"Get the last 100 lines of logs from the postgres pod"},
			"k8s_agent-get_endpoints": {"Check if the database service has healthy endpoints"},
		},
	}

	if err := agentutil.ServeWithTracing(ctx, k8sAgent, cfg, traceStore, cardOpts); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func createTools() ([]tool.Tool, error) {
	getPodsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_pods",
		Description: "List Kubernetes pods in a namespace with optional label filtering. Shows pod status, restarts, age, and node placement.",
	}, getPodsTool)
	if err != nil {
		return nil, err
	}

	getServiceToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_service",
		Description: "List Kubernetes services showing type, cluster IP, external IP, and ports. Use to check LoadBalancer status and port mappings.",
	}, getServiceTool)
	if err != nil {
		return nil, err
	}

	describeServiceToolDef, err := functiontool.New(functiontool.Config{
		Name:        "describe_service",
		Description: "Get detailed information about a Kubernetes service including selectors, endpoints, and events.",
	}, describeServiceTool)
	if err != nil {
		return nil, err
	}

	getEndpointsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_endpoints",
		Description: "List Kubernetes endpoints to verify which pod IPs are registered as backends for a service. Empty endpoints indicate selector mismatch or no ready pods.",
	}, getEndpointsTool)
	if err != nil {
		return nil, err
	}

	getEventsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_events",
		Description: "List Kubernetes events sorted by time. Useful for finding warnings, errors, and recent changes that might explain issues.",
	}, getEventsTool)
	if err != nil {
		return nil, err
	}

	getPodLogsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_pod_logs",
		Description: "Retrieve logs from a Kubernetes pod. Can get logs from specific containers and previous crashed instances.",
	}, getPodLogsTool)
	if err != nil {
		return nil, err
	}

	describePodToolDef, err := functiontool.New(functiontool.Config{
		Name:        "describe_pod",
		Description: "Get detailed information about a Kubernetes pod including status, conditions, events, and container details.",
	}, describePodTool)
	if err != nil {
		return nil, err
	}

	getNodesToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_nodes",
		Description: "List Kubernetes cluster nodes showing status, roles, age, version, and resource capacity.",
	}, getNodesTool)
	if err != nil {
		return nil, err
	}

	deletePodToolDef, err := functiontool.New(functiontool.Config{
		Name:        "delete_pod",
		Description: "Delete a Kubernetes pod by name. The deployment controller will reschedule it. Use to restart a stuck or crash-looping pod without affecting other replicas.",
	}, deletePodTool)
	if err != nil {
		return nil, err
	}

	restartDeploymentToolDef, err := functiontool.New(functiontool.Config{
		Name:        "restart_deployment",
		Description: "Perform a rolling restart of all pods in a deployment (kubectl rollout restart). Replaces pods one at a time to avoid downtime. Use when all replicas are unhealthy or after a config change.",
	}, restartDeploymentTool)
	if err != nil {
		return nil, err
	}

	scaleDeploymentToolDef, err := functiontool.New(functiontool.Config{
		Name:        "scale_deployment",
		Description: "Scale a deployment to the specified number of replicas. Can scale up to add capacity or scale down (including to 0) to stop workloads. Use with caution — scaling to 0 causes downtime.",
	}, scaleDeploymentTool)
	if err != nil {
		return nil, err
	}

	return []tool.Tool{
		getPodsToolDef,
		getServiceToolDef,
		describeServiceToolDef,
		getEndpointsToolDef,
		getEventsToolDef,
		getPodLogsToolDef,
		describePodToolDef,
		getNodesToolDef,
		deletePodToolDef,
		restartDeploymentToolDef,
		scaleDeploymentToolDef,
	}, nil
}

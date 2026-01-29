// Package main implements the Kubernetes troubleshooting agent.
// It exposes kubectl-based tools via the A2A protocol for diagnosing
// infrastructure issues in Kubernetes environments.
package main

import (
	"context"
	"log/slog"
	"os"

	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	"helpdesk/prompts"
)

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1102")
	ctx := context.Background()

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

	k8sAgent, err := llmagent.New(llmagent.Config{
		Name:        "k8s_agent",
		Description: "Kubernetes troubleshooting agent that can inspect pods, services, endpoints, events, and logs to diagnose infrastructure issues.",
		Instruction: prompts.K8s,
		Model:       llmModel,
		Tools:       tools,
	})
	if err != nil {
		slog.Error("failed to create k8s agent", "err", err)
		os.Exit(1)
	}

	if err := agentutil.Serve(ctx, k8sAgent, cfg); err != nil {
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

	return []tool.Tool{
		getPodsToolDef,
		getServiceToolDef,
		describeServiceToolDef,
		getEndpointsToolDef,
		getEventsToolDef,
		getPodLogsToolDef,
		describePodToolDef,
		getNodesToolDef,
	}, nil
}

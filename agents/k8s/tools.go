package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/tool"

	"helpdesk/internal/audit"
)

// toolAuditor is set during initialization if auditing is enabled.
var toolAuditor *audit.ToolAuditor

// resolveNamespace checks if the input looks like a database name from the
// infrastructure config and returns the associated K8s namespace. If not found
// or not a database name, returns the input unchanged.
func resolveNamespace(namespaceOrDBName string) string {
	namespaceOrDBName = strings.TrimSpace(namespaceOrDBName)
	if namespaceOrDBName == "" {
		return namespaceOrDBName
	}

	// Try to look up as a database name in the infrastructure config
	if infraConfig != nil {
		if db, ok := infraConfig.DBServers[namespaceOrDBName]; ok {
			if db.K8sNamespace != "" {
				slog.Info("resolved database name to namespace", "name", namespaceOrDBName, "namespace", db.K8sNamespace)
				return db.K8sNamespace
			}
		}
	}

	return namespaceOrDBName
}

// resolveContext checks if the input looks like a database name from the
// infrastructure config and returns the associated K8s context. If not found
// or not a database name, returns the input unchanged.
func resolveContext(contextOrDBName string) string {
	contextOrDBName = strings.TrimSpace(contextOrDBName)

	// Try to look up as a database name in the infrastructure config
	if infraConfig != nil {
		if db, ok := infraConfig.DBServers[contextOrDBName]; ok {
			if db.K8sCluster != "" {
				if cluster, ok := infraConfig.K8sClusters[db.K8sCluster]; ok {
					slog.Info("resolved database name to context", "name", contextOrDBName, "context", cluster.Context)
					return cluster.Context
				}
			}
		}
	}

	return contextOrDBName
}

// diagnoseKubectlError examines kubectl output for common failure patterns and returns
// a clear, actionable error message alongside the raw output.
func diagnoseKubectlError(output string) string {
	out := strings.ToLower(output)

	switch {
	case strings.Contains(out, "does not exist") && strings.Contains(out, "context"):
		return "The specified Kubernetes context does not exist in the local kubeconfig. " +
			"Run 'kubectl config get-contexts' to list available contexts, " +
			"or check that the correct kubeconfig file is being used."

	case strings.Contains(out, "connection refused"):
		return "Connection refused by the Kubernetes API server. " +
			"The cluster may be down, the API server address may be wrong, " +
			"or a VPN/tunnel may need to be active."

	case strings.Contains(out, "unable to connect to the server"):
		return "Cannot reach the Kubernetes API server. " +
			"Check network connectivity, verify the cluster is running, " +
			"and confirm the server address in kubeconfig is correct."

	case strings.Contains(out, "unauthorized") || strings.Contains(out, "you must be logged in"):
		return "Authentication to the cluster failed. " +
			"Credentials may have expired. Try re-authenticating (e.g., 'gcloud container clusters get-credentials' for GKE)."

	case strings.Contains(out, "forbidden"):
		return "Permission denied. The current user/service account does not have " +
			"the required RBAC permissions for this operation."

	case strings.Contains(out, "not found") && strings.Contains(out, "namespace"):
		return "The specified namespace does not exist in this cluster. " +
			"Run 'kubectl get namespaces' to list available namespaces."

	case strings.Contains(out, "not found") && strings.Contains(out, "error from server"):
		return "The requested resource was not found in the cluster. " +
			"Verify the resource name, namespace, and that it has been created."

	case strings.Contains(out, "executable file not found") || strings.Contains(out, "command not found"):
		return "kubectl is not installed or not in the system PATH. " +
			"Install kubectl and ensure it is accessible."

	case strings.Contains(out, "invalid configuration") || strings.Contains(out, "no configuration"):
		return "The kubeconfig file is invalid or missing. " +
			"Check that ~/.kube/config exists and is correctly formatted, " +
			"or set KUBECONFIG to point to the right file."

	case strings.Contains(out, "i/o timeout") || strings.Contains(out, "deadline exceeded"):
		return "Request to the Kubernetes API server timed out. " +
			"The cluster may be under heavy load, or there may be network issues."

	case strings.Contains(out, "certificate") && (strings.Contains(out, "expired") || strings.Contains(out, "invalid") || strings.Contains(out, "unknown authority")):
		return "TLS certificate error communicating with the cluster. " +
			"The cluster certificate may have expired or the CA is not trusted. " +
			"Re-fetch cluster credentials or update the kubeconfig."

	default:
		return ""
	}
}

// runKubectl executes a kubectl command and returns the output.
// If context is non-empty, it's passed as --context to kubectl.
// The provided ctx controls cancellation — if it expires, kubectl is killed.
func runKubectl(ctx context.Context, kubeContext string, args ...string) (string, error) {
	return runKubectlWithToolName(ctx, kubeContext, "", args...)
}

// runKubectlWithToolName executes a kubectl command with auditing support.
// toolName is used for audit logging; if empty, no audit is recorded.
func runKubectlWithToolName(ctx context.Context, kubeContext, toolName string, args ...string) (string, error) {
	start := time.Now()

	prefix := []string{"--request-timeout=10s"}
	if kubeContext != "" {
		prefix = append(prefix, "--context", kubeContext)
	}
	fullArgs := append(prefix, args...)
	cmd := exec.CommandContext(ctx, "kubectl", fullArgs...)
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	// Build raw command for audit logging
	rawCommand := "kubectl " + strings.Join(fullArgs, " ")

	// Audit the tool execution
	if toolAuditor != nil && toolName != "" {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       toolName,
			Parameters: map[string]any{"context": kubeContext, "args": args},
			RawCommand: rawCommand,
		}, audit.ToolResult{
			Output: truncateForAudit(string(output), 500),
			Error:  errMsg,
		}, duration)
	}

	// Log successful tool execution at INFO level
	if err == nil && toolName != "" {
		slog.Info("tool ok", "name", toolName, "ms", duration.Milliseconds())
	}

	if err != nil {
		out := strings.TrimSpace(string(output))
		if out == "" {
			out = "(no output from kubectl)"
		}
		slog.Error("kubectl command failed", "tool", toolName, "args", args, "ms", duration.Milliseconds(), "err", err)
		if ctx.Err() != nil {
			return "", fmt.Errorf("kubectl timed out or was cancelled: %v\nOutput: %s", ctx.Err(), out)
		}
		if diagnosis := diagnoseKubectlError(out); diagnosis != "" {
			return "", fmt.Errorf("%s\n\nRaw error: %s", diagnosis, out)
		}
		return "", fmt.Errorf("kubectl failed: %v\nOutput: %s", err, out)
	}
	return string(output), nil
}

// truncateForAudit truncates a string for audit logging.
func truncateForAudit(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// recordClientGoAudit records audit events for client-go based operations.
func recordClientGoAudit(ctx context.Context, toolName string, params map[string]any, resultCount int, err error, duration time.Duration) {
	// Log at INFO level
	if err == nil {
		slog.Info("tool ok", "name", toolName, "count", resultCount, "ms", duration.Milliseconds())
	} else {
		slog.Error("tool failed", "name", toolName, "ms", duration.Milliseconds(), "err", err)
	}

	// Record to audit store
	if toolAuditor == nil {
		return
	}

	var errMsg string
	if err != nil {
		errMsg = err.Error()
	}

	toolAuditor.RecordToolCall(ctx, audit.ToolCall{
		Name:       toolName,
		Parameters: params,
		RawCommand: fmt.Sprintf("client-go: %s", toolName),
	}, audit.ToolResult{
		Output: fmt.Sprintf("returned %d items", resultCount),
		Error:  errMsg,
	}, duration)
}

// KubectlResult is the standard output type for all kubectl tools.
type KubectlResult struct {
	Output string `json:"output"`
}

// GetPodsArgs defines arguments for the get_pods tool.
type GetPodsArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace string `json:"namespace" jsonschema:"The Kubernetes namespace to list pods from. Use 'all' for all namespaces."`
	Labels    string `json:"labels,omitempty" jsonschema:"Optional label selector to filter pods (e.g., 'app=postgres')."`
}

func getPodsTool(ctx tool.Context, args GetPodsArgs) (GetPodsResult, error) {
	// Resolve database name to namespace/context if applicable
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)

	start := time.Now()
	result, err := fetchPods(ctx, kubeContext, namespace, args.Labels)
	duration := time.Since(start)

	recordClientGoAudit(ctx, "get_pods", map[string]any{
		"context":   kubeContext,
		"namespace": namespace,
		"labels":    args.Labels,
	}, result.Count, err, duration)

	return result, err
}

// GetServiceArgs defines arguments for the get_service tool.
type GetServiceArgs struct {
	Context     string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace   string `json:"namespace" jsonschema:"The Kubernetes namespace to list services from."`
	ServiceName string `json:"service_name,omitempty" jsonschema:"Optional specific service name to get. If empty, lists all services."`
	ServiceType string `json:"service_type,omitempty" jsonschema:"Optional filter by service type: ClusterIP, NodePort, LoadBalancer."`
}

func getServiceTool(ctx tool.Context, args GetServiceArgs) (GetServiceResult, error) {
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)

	start := time.Now()
	result, err := fetchServices(ctx, kubeContext, namespace, args.ServiceName, args.ServiceType)
	duration := time.Since(start)

	recordClientGoAudit(ctx, "get_service", map[string]any{
		"context":      kubeContext,
		"namespace":    namespace,
		"service_name": args.ServiceName,
		"service_type": args.ServiceType,
	}, result.Count, err, duration)

	return result, err
}

// DescribeServiceArgs defines arguments for the describe_service tool.
type DescribeServiceArgs struct {
	Context     string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace   string `json:"namespace" jsonschema:"The Kubernetes namespace of the service."`
	ServiceName string `json:"service_name" jsonschema:"The name of the service to describe."`
}

func describeServiceTool(ctx tool.Context, args DescribeServiceArgs) (KubectlResult, error) {
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)
	cmdArgs := []string{"describe", "svc", args.ServiceName}

	if namespace != "" {
		cmdArgs = append(cmdArgs, "-n", namespace)
	}

	output, err := runKubectlWithToolName(ctx, kubeContext, "describe_service", cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error describing service: %v", err)
	}
	return KubectlResult{Output: output}, nil
}

// GetEndpointsArgs defines arguments for the get_endpoints tool.
type GetEndpointsArgs struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace    string `json:"namespace" jsonschema:"The Kubernetes namespace to check endpoints in."`
	EndpointName string `json:"endpoint_name,omitempty" jsonschema:"Optional specific endpoint name (usually matches service name)."`
}

func getEndpointsTool(ctx tool.Context, args GetEndpointsArgs) (GetEndpointsResult, error) {
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)

	start := time.Now()
	result, err := fetchEndpoints(ctx, kubeContext, namespace, args.EndpointName)
	duration := time.Since(start)

	recordClientGoAudit(ctx, "get_endpoints", map[string]any{
		"context":       kubeContext,
		"namespace":     namespace,
		"endpoint_name": args.EndpointName,
	}, result.Count, err, duration)

	return result, err
}

// GetEventsArgs defines arguments for the get_events tool.
type GetEventsArgs struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace    string `json:"namespace" jsonschema:"The Kubernetes namespace to get events from."`
	ResourceName string `json:"resource_name,omitempty" jsonschema:"Optional filter events related to a specific resource name."`
	EventType    string `json:"event_type,omitempty" jsonschema:"Optional filter by event type: Normal or Warning."`
}

func getEventsTool(ctx tool.Context, args GetEventsArgs) (GetEventsResult, error) {
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)

	start := time.Now()
	result, err := fetchEvents(ctx, kubeContext, namespace, args.ResourceName, args.EventType)
	duration := time.Since(start)

	recordClientGoAudit(ctx, "get_events", map[string]any{
		"context":       kubeContext,
		"namespace":     namespace,
		"resource_name": args.ResourceName,
		"event_type":    args.EventType,
	}, result.Count, err, duration)

	return result, err
}

// GetPodLogsArgs defines arguments for the get_pod_logs tool.
type GetPodLogsArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace string `json:"namespace,omitempty" jsonschema:"The Kubernetes namespace of the pod (e.g., 'default', 'kube-system')."`
	PodName   string `json:"pod_name" jsonschema:"required,The exact pod name to get logs from (e.g., 'nginx-7d6877d777-abc12')."`
	Container string `json:"container,omitempty" jsonschema:"Container name, only needed if pod has multiple containers."`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"Number of recent log lines to retrieve (default 50)."`
	Previous  bool   `json:"previous,omitempty" jsonschema:"If true, get logs from the previous container instance (useful for crash loops)."`
}

func getPodLogsTool(ctx tool.Context, args GetPodLogsArgs) (KubectlResult, error) {
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)
	cmdArgs := []string{"logs", args.PodName}

	if namespace != "" {
		cmdArgs = append(cmdArgs, "-n", namespace)
	}

	if args.Container != "" {
		cmdArgs = append(cmdArgs, "-c", args.Container)
	}

	tailLines := args.TailLines
	if tailLines <= 0 {
		tailLines = 50
	}
	cmdArgs = append(cmdArgs, "--tail", strconv.Itoa(tailLines))

	if args.Previous {
		cmdArgs = append(cmdArgs, "--previous")
	}

	output, err := runKubectlWithToolName(ctx, kubeContext, "get_pod_logs", cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error getting pod logs: %v", err)
	}
	if strings.TrimSpace(output) == "" {
		return KubectlResult{Output: "No logs available for this pod."}, nil
	}
	return KubectlResult{Output: output}, nil
}

// DescribePodArgs defines arguments for the describe_pod tool.
type DescribePodArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace string `json:"namespace,omitempty" jsonschema:"The Kubernetes namespace of the pod (e.g., 'default', 'kube-system')."`
	PodName   string `json:"pod_name" jsonschema:"required,The exact pod name to describe."`
}

func describePodTool(ctx tool.Context, args DescribePodArgs) (KubectlResult, error) {
	namespace := resolveNamespace(args.Namespace)
	kubeContext := resolveContext(args.Context)
	cmdArgs := []string{"describe", "pod", args.PodName}

	if namespace != "" {
		cmdArgs = append(cmdArgs, "-n", namespace)
	}

	output, err := runKubectlWithToolName(ctx, kubeContext, "describe_pod", cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error describing pod: %v", err)
	}
	return KubectlResult{Output: output}, nil
}

// GetNodesArgs defines arguments for the get_nodes tool.
type GetNodesArgs struct {
	Context    string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	ShowLabels bool   `json:"show_labels,omitempty" jsonschema:"If true, show node labels in output."`
}

func getNodesTool(ctx tool.Context, args GetNodesArgs) (GetNodesResult, error) {
	kubeContext := resolveContext(args.Context)

	start := time.Now()
	result, err := fetchNodes(ctx, kubeContext, args.ShowLabels)
	duration := time.Since(start)

	recordClientGoAudit(ctx, "get_nodes", map[string]any{
		"context":     kubeContext,
		"show_labels": args.ShowLabels,
	}, result.Count, err, duration)

	return result, err
}

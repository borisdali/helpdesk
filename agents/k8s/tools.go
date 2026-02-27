package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/policy"
)

// toolAuditor is set during initialization if auditing is enabled.
var toolAuditor *audit.ToolAuditor

// policyEnforcer is set during initialization for policy enforcement.
var policyEnforcer *agentutil.PolicyEnforcer

// namespaceInfo holds resolved namespace information for policy checks.
type namespaceInfo struct {
	Namespace string
	Tags      []string
}

// resolveNamespaceInfo resolves a namespace or database name to full info.
// When infraConfig is set and the namespace is not registered (neither as a
// database name nor as a K8s namespace of a registered database), returns an
// error (hard reject) so callers can fail before any tool execution.
func resolveNamespaceInfo(namespaceOrDBName string) (namespaceInfo, error) {
	namespaceOrDBName = strings.TrimSpace(namespaceOrDBName)
	if namespaceOrDBName == "" {
		return namespaceInfo{Namespace: namespaceOrDBName}, nil
	}

	if infraConfig != nil {
		// Check if input is a registered database name with a K8s namespace.
		if db, ok := infraConfig.DBServers[namespaceOrDBName]; ok {
			if db.K8sNamespace != "" {
				slog.Info("resolved database name to namespace", "name", namespaceOrDBName, "namespace", db.K8sNamespace)
				return namespaceInfo{
					Namespace: db.K8sNamespace,
					Tags:      db.Tags,
				}, nil
			}
		}
		// Check if input is the actual K8s namespace of a registered database.
		for _, db := range infraConfig.DBServers {
			if db.K8sNamespace == namespaceOrDBName {
				return namespaceInfo{
					Namespace: namespaceOrDBName,
					Tags:      db.Tags,
				}, nil
			}
		}
		// infraConfig is set but namespace not registered — hard reject.
		known := make([]string, 0, len(infraConfig.DBServers))
		for id := range infraConfig.DBServers {
			known = append(known, id)
		}
		sort.Strings(known)
		return namespaceInfo{}, fmt.Errorf(
			"namespace or database %q not registered in infrastructure config; "+
				"contact your IT administrator to add it. Known databases: %s",
			namespaceOrDBName, strings.Join(known, ", "))
	}

	// Dev mode: no infra config — return as-is.
	return namespaceInfo{Namespace: namespaceOrDBName}, nil
}

// resolveNamespace checks if the input looks like a database name from the
// infrastructure config and returns the associated K8s namespace. If not found
// or not a database name, returns the input unchanged.
func resolveNamespace(namespaceOrDBName string) (string, error) {
	info, err := resolveNamespaceInfo(namespaceOrDBName)
	if err != nil {
		return "", err
	}
	return info.Namespace, nil
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

// checkK8sPolicy checks if a kubernetes operation is allowed by policy.
// Returns nil if allowed, error if denied.
func checkK8sPolicy(ctx context.Context, namespace string, action policy.ActionClass, tags []string) error {
	if policyEnforcer == nil {
		return nil
	}
	return policyEnforcer.CheckKubernetes(ctx, namespace, action, tags, "")
}

// parsePodsAffected counts the number of Kubernetes resources modified from
// kubectl output. Handles the standard kubectl confirmation lines:
//
//	pod "foo" deleted
//	deployment.apps "bar" configured
//	service "baz" created
func parsePodsAffected(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, " deleted") ||
			strings.HasSuffix(line, " configured") ||
			strings.HasSuffix(line, " created") {
			count++
		}
	}
	return count
}

// checkK8sPolicyResult runs a post-execution policy check for a Kubernetes
// operation, enforcing blast-radius conditions with the actual resource count.
// Call this after write or destructive kubectl commands.
func checkK8sPolicyResult(ctx context.Context, namespace string, action policy.ActionClass, tags []string, output string, execErr error) error {
	if policyEnforcer == nil {
		return nil
	}
	return policyEnforcer.CheckKubernetesResult(ctx, namespace, action, tags, agentutil.ToolOutcome{
		PodsAffected: parsePodsAffected(output),
		Err:          execErr,
	})
}

// runKubectl is the kubectl execution function. Tests replace this variable to
// inject mock output without spawning a real kubectl process.
var runKubectl = runKubectlExec

// runKubectlExec is the production kubectl runner: it prepends the timeout and
// optional context flags, invokes kubectl, and returns structured errors.
func runKubectlExec(ctx context.Context, kubeContext string, args ...string) (string, error) {
	prefix := []string{"--request-timeout=10s"}
	if kubeContext != "" {
		prefix = append(prefix, "--context", kubeContext)
	}
	fullArgs := append(prefix, args...)
	output, err := exec.CommandContext(ctx, "kubectl", fullArgs...).CombinedOutput()
	if err != nil {
		out := strings.TrimSpace(string(output))
		if out == "" {
			out = "(no output from kubectl)"
		}
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

// runKubectlWithToolName wraps runKubectl with timing, audit logging, and slog.
// toolName is used for audit logging; if empty, no audit is recorded.
func runKubectlWithToolName(ctx context.Context, kubeContext, toolName string, args ...string) (string, error) {
	start := time.Now()
	output, err := runKubectl(ctx, kubeContext, args...)
	duration := time.Since(start)

	rawCommand := "kubectl " + strings.Join(args, " ")
	if kubeContext != "" {
		rawCommand = "kubectl --context " + kubeContext + " " + strings.Join(args, " ")
	}

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
			Output: truncateForAudit(output, 500),
			Error:  errMsg,
		}, duration)
	}

	if err == nil && toolName != "" {
		slog.Info("tool ok", "name", toolName, "ms", duration.Milliseconds())
	}
	if err != nil {
		slog.Error("kubectl command failed", "tool", toolName, "args", args, "ms", duration.Milliseconds(), "err", err)
	}
	return output, err
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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return GetPodsResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		slog.Warn("policy denied kubernetes access",
			"tool", "get_pods",
			"namespace", namespace,
			"tags", nsInfo.Tags,
			"err", err)
		return GetPodsResult{}, fmt.Errorf("policy denied: %w", err)
	}

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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return GetServiceResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		return GetServiceResult{}, fmt.Errorf("policy denied: %w", err)
	}

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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		return KubectlResult{}, fmt.Errorf("policy denied: %w", err)
	}

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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return GetEndpointsResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		return GetEndpointsResult{}, fmt.Errorf("policy denied: %w", err)
	}

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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return GetEventsResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		return GetEventsResult{}, fmt.Errorf("policy denied: %w", err)
	}

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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		return KubectlResult{}, fmt.Errorf("policy denied: %w", err)
	}

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
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	// Check policy before executing
	if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
		return KubectlResult{}, fmt.Errorf("policy denied: %w", err)
	}

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

	// Check policy for cluster-level access (no namespace)
	if err := checkK8sPolicy(ctx, "", policy.ActionRead, nil); err != nil {
		return GetNodesResult{}, fmt.Errorf("policy denied: %w", err)
	}

	start := time.Now()
	result, err := fetchNodes(ctx, kubeContext, args.ShowLabels)
	duration := time.Since(start)

	recordClientGoAudit(ctx, "get_nodes", map[string]any{
		"context":     kubeContext,
		"show_labels": args.ShowLabels,
	}, result.Count, err, duration)

	return result, err
}

// DeletePodArgs defines arguments for the delete_pod tool.
type DeletePodArgs struct {
	Context          string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace        string `json:"namespace" jsonschema:"required,The Kubernetes namespace of the pod."`
	PodName          string `json:"pod_name" jsonschema:"required,The exact pod name to delete. Use get_pods to find the name."`
	GracePeriodSeconds int  `json:"grace_period_seconds,omitempty" jsonschema:"Seconds for graceful termination (default: pod's terminationGracePeriodSeconds). Use 0 for immediate deletion."`
}

func deletePodTool(ctx tool.Context, args DeletePodArgs) (KubectlResult, error) {
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	if err := checkK8sPolicy(ctx, namespace, policy.ActionDestructive, nsInfo.Tags); err != nil {
		return KubectlResult{}, fmt.Errorf("policy denied: %w", err)
	}

	cmdArgs := []string{"delete", "pod", args.PodName, "-n", namespace}
	if args.GracePeriodSeconds > 0 {
		cmdArgs = append(cmdArgs, "--grace-period", strconv.Itoa(args.GracePeriodSeconds))
	}

	output, err := runKubectlWithToolName(ctx, kubeContext, "delete_pod", cmdArgs...)
	if err != nil {
		return KubectlResult{Output: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	if postErr := checkK8sPolicyResult(ctx, namespace, policy.ActionDestructive, nsInfo.Tags, output, err); postErr != nil {
		return KubectlResult{}, fmt.Errorf("policy denied after execution: %w", postErr)
	}

	return KubectlResult{Output: output}, nil
}

// RestartDeploymentArgs defines arguments for the restart_deployment tool.
type RestartDeploymentArgs struct {
	Context        string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace      string `json:"namespace" jsonschema:"required,The Kubernetes namespace of the deployment."`
	DeploymentName string `json:"deployment_name" jsonschema:"required,The name of the deployment to restart. Use get_pods or kubectl get deployments to find the name."`
}

func restartDeploymentTool(ctx tool.Context, args RestartDeploymentArgs) (KubectlResult, error) {
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	if err := checkK8sPolicy(ctx, namespace, policy.ActionDestructive, nsInfo.Tags); err != nil {
		return KubectlResult{}, fmt.Errorf("policy denied: %w", err)
	}

	cmdArgs := []string{"rollout", "restart", "deployment", args.DeploymentName, "-n", namespace}
	output, err := runKubectlWithToolName(ctx, kubeContext, "restart_deployment", cmdArgs...)
	if err != nil {
		return KubectlResult{Output: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	if postErr := checkK8sPolicyResult(ctx, namespace, policy.ActionDestructive, nsInfo.Tags, output, err); postErr != nil {
		return KubectlResult{}, fmt.Errorf("policy denied after execution: %w", postErr)
	}

	return KubectlResult{Output: output}, nil
}

// ScaleDeploymentArgs defines arguments for the scale_deployment tool.
type ScaleDeploymentArgs struct {
	Context        string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace      string `json:"namespace" jsonschema:"required,The Kubernetes namespace of the deployment."`
	DeploymentName string `json:"deployment_name" jsonschema:"required,The name of the deployment to scale."`
	Replicas       int    `json:"replicas" jsonschema:"required,Target replica count. Use 0 to scale down completely."`
}

func scaleDeploymentTool(ctx tool.Context, args ScaleDeploymentArgs) (KubectlResult, error) {
	nsInfo, err := resolveNamespaceInfo(args.Namespace)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("access denied: %w", err)
	}
	namespace := nsInfo.Namespace
	kubeContext := resolveContext(args.Context)

	if err := checkK8sPolicy(ctx, namespace, policy.ActionDestructive, nsInfo.Tags); err != nil {
		return KubectlResult{}, fmt.Errorf("policy denied: %w", err)
	}

	cmdArgs := []string{
		"scale", "deployment", args.DeploymentName,
		"--replicas", strconv.Itoa(args.Replicas),
		"-n", namespace,
	}
	output, err := runKubectlWithToolName(ctx, kubeContext, "scale_deployment", cmdArgs...)
	if err != nil {
		return KubectlResult{Output: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	if postErr := checkK8sPolicyResult(ctx, namespace, policy.ActionDestructive, nsInfo.Tags, output, err); postErr != nil {
		return KubectlResult{}, fmt.Errorf("policy denied after execution: %w", postErr)
	}

	return KubectlResult{Output: output}, nil
}

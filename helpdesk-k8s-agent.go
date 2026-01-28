// Package main implements a Kubernetes troubleshooting agent for the helpdesk system.
// It exposes kubectl-based tools via the A2A protocol for diagnosing database
// connectivity and infrastructure issues in Kubernetes environments.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adka2a"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const k8sAgentInstruction = `You are a Kubernetes troubleshooting expert. You help diagnose issues with
databases and applications running in Kubernetes clusters.

## CRITICAL: Fail fast on connectivity errors

If ANY tool call returns an error, STOP IMMEDIATELY. Do NOT retry with different
parameters or try other tools. Report the error using this exact format:

ERROR — <tool_name> failed for cluster <context>
<paste the full error message from the tool, verbatim>
This means: <one-sentence explanation>

Never paraphrase, summarize, or omit the error text. The orchestrating agent and the user
need the exact error to diagnose the problem.

## Investigation workflow (only if the cluster is reachable)

When investigating connectivity issues:
1. First check if the pods are running (get_pods)
2. Check the service configuration (get_service, describe_service)
3. Verify endpoints are registered (get_endpoints)
4. Look for recent events that might indicate problems (get_events)
5. If needed, check pod logs for errors (get_pod_logs)

If step 1 fails with a connection or authentication error, skip all remaining steps and report the failure.

For LoadBalancer services, pay attention to:
- Whether an external IP has been provisioned (look for "pending" status)
- Port mappings between the service and target pods
- Endpoint health

Always explain your findings and suggest next steps to the user.`

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
	prefix := []string{"--request-timeout=10s"}
	if kubeContext != "" {
		prefix = append(prefix, "--context", kubeContext)
	}
	args = append(prefix, args...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
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

// getPodsTool lists pods in a namespace with optional label filtering.
func getPodsTool(ctx tool.Context, args GetPodsArgs) (KubectlResult, error) {
	cmdArgs := []string{"get", "pods", "-o", "wide"}

	if args.Namespace == "all" {
		cmdArgs = append(cmdArgs, "--all-namespaces")
	} else if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	if args.Labels != "" {
		cmdArgs = append(cmdArgs, "-l", args.Labels)
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error getting pods: %v", err)
	}
	if strings.TrimSpace(output) == "" {
		return KubectlResult{Output: "No pods found matching the criteria."}, nil
	}
	return KubectlResult{Output: output}, nil
}

// GetServiceArgs defines arguments for the get_service tool.
type GetServiceArgs struct {
	Context     string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace   string `json:"namespace" jsonschema:"The Kubernetes namespace to list services from."`
	ServiceName string `json:"service_name,omitempty" jsonschema:"Optional specific service name to get. If empty, lists all services."`
	ServiceType string `json:"service_type,omitempty" jsonschema:"Optional filter by service type: ClusterIP, NodePort, LoadBalancer."`
}

// getServiceTool retrieves Kubernetes service information.
func getServiceTool(ctx tool.Context, args GetServiceArgs) (KubectlResult, error) {
	cmdArgs := []string{"get", "svc", "-o", "wide"}

	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	if args.ServiceName != "" {
		cmdArgs = append(cmdArgs, args.ServiceName)
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error getting services: %v", err)
	}

	// Filter by service type if specified
	if args.ServiceType != "" && args.ServiceName == "" {
		lines := strings.Split(output, "\n")
		var filtered []string
		for i, line := range lines {
			if i == 0 || strings.Contains(line, args.ServiceType) {
				filtered = append(filtered, line)
			}
		}
		output = strings.Join(filtered, "\n")
	}

	if strings.TrimSpace(output) == "" {
		return KubectlResult{Output: "No services found matching the criteria."}, nil
	}
	return KubectlResult{Output: output}, nil
}

// DescribeServiceArgs defines arguments for the describe_service tool.
type DescribeServiceArgs struct {
	Context     string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace   string `json:"namespace" jsonschema:"The Kubernetes namespace of the service."`
	ServiceName string `json:"service_name" jsonschema:"The name of the service to describe."`
}

// describeServiceTool provides detailed information about a specific service.
func describeServiceTool(ctx tool.Context, args DescribeServiceArgs) (KubectlResult, error) {
	cmdArgs := []string{"describe", "svc", args.ServiceName}

	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
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

// getEndpointsTool retrieves endpoint information to verify backend pod connectivity.
func getEndpointsTool(ctx tool.Context, args GetEndpointsArgs) (KubectlResult, error) {
	cmdArgs := []string{"get", "endpoints", "-o", "wide"}

	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	if args.EndpointName != "" {
		cmdArgs = append(cmdArgs, args.EndpointName)
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error getting endpoints: %v", err)
	}
	if strings.TrimSpace(output) == "" {
		return KubectlResult{Output: "No endpoints found. This may indicate no pods match the service selector."}, nil
	}
	return KubectlResult{Output: output}, nil
}

// GetEventsArgs defines arguments for the get_events tool.
type GetEventsArgs struct {
	Context      string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace    string `json:"namespace" jsonschema:"The Kubernetes namespace to get events from."`
	ResourceName string `json:"resource_name,omitempty" jsonschema:"Optional filter events related to a specific resource name."`
	EventType    string `json:"event_type,omitempty" jsonschema:"Optional filter by event type: Normal or Warning."`
}

// getEventsTool retrieves Kubernetes events for troubleshooting.
func getEventsTool(ctx tool.Context, args GetEventsArgs) (KubectlResult, error) {
	cmdArgs := []string{"get", "events", "--sort-by=.lastTimestamp"}

	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	if args.EventType != "" {
		cmdArgs = append(cmdArgs, "--field-selector", fmt.Sprintf("type=%s", args.EventType))
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error getting events: %v", err)
	}

	// Filter by resource name if specified
	if args.ResourceName != "" {
		lines := strings.Split(output, "\n")
		var filtered []string
		for i, line := range lines {
			if i == 0 || strings.Contains(line, args.ResourceName) {
				filtered = append(filtered, line)
			}
		}
		output = strings.Join(filtered, "\n")
	}

	if strings.TrimSpace(output) == "" {
		return KubectlResult{Output: "No events found matching the criteria."}, nil
	}
	return KubectlResult{Output: output}, nil
}

// GetPodLogsArgs defines arguments for the get_pod_logs tool.
type GetPodLogsArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"Kubernetes context to use. If empty, uses current context."`
	Namespace string `json:"namespace" jsonschema:"The Kubernetes namespace of the pod."`
	PodName   string `json:"pod_name" jsonschema:"The name of the pod to get logs from."`
	Container string `json:"container,omitempty" jsonschema:"Optional container name if pod has multiple containers."`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"Number of recent log lines to retrieve (default 50)."`
	Previous  bool   `json:"previous,omitempty" jsonschema:"If true, get logs from the previous container instance (useful for crash loops)."`
}

// getPodLogsTool retrieves logs from a specific pod.
func getPodLogsTool(ctx tool.Context, args GetPodLogsArgs) (KubectlResult, error) {
	cmdArgs := []string{"logs", args.PodName}

	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
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

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
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
	Namespace string `json:"namespace" jsonschema:"The Kubernetes namespace of the pod."`
	PodName   string `json:"pod_name" jsonschema:"The name of the pod to describe."`
}

// describePodTool provides detailed information about a specific pod.
func describePodTool(ctx tool.Context, args DescribePodArgs) (KubectlResult, error) {
	cmdArgs := []string{"describe", "pod", args.PodName}

	if args.Namespace != "" {
		cmdArgs = append(cmdArgs, "-n", args.Namespace)
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
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

// getNodesTool retrieves information about cluster nodes.
func getNodesTool(ctx tool.Context, args GetNodesArgs) (KubectlResult, error) {
	cmdArgs := []string{"get", "nodes", "-o", "wide"}

	if args.ShowLabels {
		cmdArgs = append(cmdArgs, "--show-labels")
	}

	output, err := runKubectl(ctx, args.Context, cmdArgs...)
	if err != nil {
		return KubectlResult{}, fmt.Errorf("error getting nodes: %v", err)
	}
	return KubectlResult{Output: output}, nil
}

// newK8sAgent creates the Kubernetes troubleshooting agent with kubectl tools.
func newK8sAgent(ctx context.Context, modelVendor, modelName, apiKey string) (agent.Agent, error) {
	// Create kubectl tools
	getPodsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_pods",
		Description: "List Kubernetes pods in a namespace with optional label filtering. Shows pod status, restarts, age, and node placement.",
	}, getPodsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_pods tool: %v", err)
	}

	getServiceToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_service",
		Description: "List Kubernetes services showing type, cluster IP, external IP, and ports. Use to check LoadBalancer status and port mappings.",
	}, getServiceTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_service tool: %v", err)
	}

	describeServiceToolDef, err := functiontool.New(functiontool.Config{
		Name:        "describe_service",
		Description: "Get detailed information about a Kubernetes service including selectors, endpoints, and events.",
	}, describeServiceTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create describe_service tool: %v", err)
	}

	getEndpointsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_endpoints",
		Description: "List Kubernetes endpoints to verify which pod IPs are registered as backends for a service. Empty endpoints indicate selector mismatch or no ready pods.",
	}, getEndpointsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_endpoints tool: %v", err)
	}

	getEventsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_events",
		Description: "List Kubernetes events sorted by time. Useful for finding warnings, errors, and recent changes that might explain issues.",
	}, getEventsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_events tool: %v", err)
	}

	getPodLogsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_pod_logs",
		Description: "Retrieve logs from a Kubernetes pod. Can get logs from specific containers and previous crashed instances.",
	}, getPodLogsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_pod_logs tool: %v", err)
	}

	describePodToolDef, err := functiontool.New(functiontool.Config{
		Name:        "describe_pod",
		Description: "Get detailed information about a Kubernetes pod including status, conditions, events, and container details.",
	}, describePodTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create describe_pod tool: %v", err)
	}

	getNodesToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_nodes",
		Description: "List Kubernetes cluster nodes showing status, roles, age, version, and resource capacity.",
	}, getNodesTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_nodes tool: %v", err)
	}

	// Create the LLM model based on vendor
	var llmModel model.LLM
	switch strings.ToLower(modelVendor) {
	case "google", "gemini":
		llmModel, err = gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
		if err != nil {
			return nil, fmt.Errorf("failed to create Gemini model: %v", err)
		}
		slog.Info("using model", "vendor", "gemini", "model", modelName)
	case "anthropic":
		llmModel, err = NewAnthropicModel(ctx, modelName, apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create Anthropic model: %v", err)
		}
		slog.Info("using model", "vendor", "anthropic", "model", modelName)
	default:
		return nil, fmt.Errorf("unknown LLM model vendor: %s (supported: google, gemini, anthropic)", modelVendor)
	}

	// Create the k8s agent with all tools
	k8sAgent, err := llmagent.New(llmagent.Config{
		Name:        "k8s_agent",
		Description: "Kubernetes troubleshooting agent that can inspect pods, services, endpoints, events, and logs to diagnose infrastructure issues.",
		Instruction: k8sAgentInstruction,
		Model:       llmModel,
		Tools: []tool.Tool{
			getPodsToolDef,
			getServiceToolDef,
			describeServiceToolDef,
			getEndpointsToolDef,
			getEventsToolDef,
			getPodLogsToolDef,
			describePodToolDef,
			getNodesToolDef,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s agent: %v", err)
	}

	return k8sAgent, nil
}

// startK8sAgentServer starts an HTTP server exposing the k8s-agent via A2A protocol.
func startK8sAgentServer(ctx context.Context, listenAddr, modelVendor, modelName, apiKey string) (string, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", fmt.Errorf("failed to bind to port: %v", err)
	}

	baseURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}

	slog.Info("starting K8s A2A server", "url", baseURL.String())

	k8sAgent, err := newK8sAgent(ctx, modelVendor, modelName, apiKey)
	if err != nil {
		return "", fmt.Errorf("failed to create k8s agent: %v", err)
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               k8sAgent.Name(),
		Description:        "Kubernetes troubleshooting agent with kubectl tools for diagnosing infrastructure issues.",
		Skills:             adka2a.BuildAgentSkills(k8sAgent),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        k8sAgent.Name(),
			Agent:          k8sAgent,
			SessionService: session.InMemoryService(),
		},
	})
	requestHandler := a2asrv.NewHandler(executor)
	mux.Handle(agentPath, a2asrv.NewJSONRPCHandler(requestHandler))

	slog.Info("agent card available", "url", baseURL.String()+"/.well-known/agent-card.json")

	err = http.Serve(listener, mux)

	slog.Warn("K8s A2A server stopped", "err", err)
	return baseURL.String(), nil
}

func main() {
	initLogging(os.Args[1:])

	ctx := context.Background()
	modelVendor := os.Getenv("HELPDESK_MODEL_VENDOR")
	modelName := os.Getenv("HELPDESK_MODEL_NAME")
	apiKey := os.Getenv("HELPDESK_API_KEY")
	if modelVendor == "" || modelName == "" || apiKey == "" {
		slog.Error("missing required environment variables: HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY")
		os.Exit(1)
	}

	// Listen address: defaults to localhost:1102
	listenAddr := os.Getenv("HELPDESK_AGENT_ADDR")
	if listenAddr == "" {
		listenAddr = "localhost:1102"
	}

	serverURL, err := startK8sAgentServer(ctx, listenAddr, modelVendor, modelName, apiKey)
	if err != nil {
		slog.Error("failed to start K8s A2A server", "err", err)
		os.Exit(1)
	}
	slog.Info("K8s A2A server started", "url", serverURL)
}

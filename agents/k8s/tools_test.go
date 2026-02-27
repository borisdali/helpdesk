package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	"helpdesk/agentutil"
	"helpdesk/internal/infra"
)

// mockK8sToolContext implements tool.Context for k8s agent tests.
type mockK8sToolContext struct {
	context.Context
}

func (mockK8sToolContext) UserContent() *genai.Content                                          { return nil }
func (mockK8sToolContext) InvocationID() string                                                 { return "test-invocation" }
func (mockK8sToolContext) AgentName() string                                                    { return "k8s_agent" }
func (mockK8sToolContext) ReadonlyState() session.ReadonlyState                                 { return nil }
func (mockK8sToolContext) UserID() string                                                       { return "test-user" }
func (mockK8sToolContext) AppName() string                                                      { return "test-app" }
func (mockK8sToolContext) SessionID() string                                                    { return "test-session" }
func (mockK8sToolContext) Branch() string                                                       { return "" }
func (mockK8sToolContext) Artifacts() agent.Artifacts                                           { return nil }
func (mockK8sToolContext) State() session.State                                                 { return nil }
func (mockK8sToolContext) FunctionCallID() string                                               { return "test-call-id" }
func (mockK8sToolContext) Actions() *session.EventActions                                       { return nil }
func (mockK8sToolContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) { return nil, nil }
func (mockK8sToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation                 { return nil }
func (mockK8sToolContext) RequestConfirmation(string, any) error                                { return nil }

func newK8sTestContext() tool.Context {
	return mockK8sToolContext{context.Background()}
}

// withMockKubectl temporarily replaces the runKubectl variable for a test.
// Returns a cleanup function that restores the original.
func withMockKubectl(output string, err error) func() {
	orig := runKubectl
	runKubectl = func(_ context.Context, _ string, _ ...string) (string, error) {
		return output, err
	}
	return func() { runKubectl = orig }
}

// withK8sPolicyEnforcer temporarily sets the package-level policyEnforcer.
func withK8sPolicyEnforcer(e *agentutil.PolicyEnforcer) func() {
	old := policyEnforcer
	policyEnforcer = e
	return func() { policyEnforcer = old }
}

// writeTempK8sPolicyFile writes a YAML policy to a temp file and returns its path.
func writeTempK8sPolicyFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "k8s-policies-*.yaml")
	if err != nil {
		t.Fatalf("create temp policy file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp policy file: %v", err)
	}
	f.Close()
	return f.Name()
}

// newDenyK8sDestructiveEnforcer creates a PolicyEnforcer that denies destructive k8s operations.
func newDenyK8sDestructiveEnforcer(t *testing.T) *agentutil.PolicyEnforcer {
	t.Helper()
	const yaml = `
version: "1"
policies:
  - name: deny-k8s-destructive
    resources:
      - type: kubernetes
    rules:
      - action: destructive
        effect: deny
        message: "destructive kubernetes operations are not permitted in this test"
`
	path := writeTempK8sPolicyFile(t, yaml)
	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    path,
		DefaultPolicy: "allow",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

// newK8sBlastRadiusEnforcer creates a PolicyEnforcer that allows destructive ops
// but limits the number of pods affected.
func newK8sBlastRadiusEnforcer(t *testing.T, maxPods int) *agentutil.PolicyEnforcer {
	t.Helper()
	yamlContent := fmt.Sprintf(`
version: "1"
policies:
  - name: k8s-blast-radius
    resources:
      - type: kubernetes
    rules:
      - action: destructive
        effect: allow
        conditions:
          max_pods_affected: %d
`, maxPods)
	path := writeTempK8sPolicyFile(t, yamlContent)
	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    path,
		DefaultPolicy: "deny",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

func TestDiagnoseKubectlError(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantSub string // expected substring in result; empty = no diagnosis
	}{
		{
			name:    "context does not exist",
			output:  `error: context "bad-ctx" does not exist in the kubeconfig`,
			wantSub: "context does not exist",
		},
		{
			name:    "connection refused",
			output:  "dial tcp 127.0.0.1:6443: connection refused",
			wantSub: "Connection refused",
		},
		{
			name:    "unable to connect",
			output:  "Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout",
			wantSub: "Cannot reach",
		},
		{
			name:    "unauthorized",
			output:  "error: You must be logged in to the server (Unauthorized)",
			wantSub: "Authentication to the cluster failed",
		},
		{
			name:    "forbidden",
			output:  `Error from server (Forbidden): pods is forbidden`,
			wantSub: "Permission denied",
		},
		{
			name:    "namespace not found",
			output:  `Error from server (NotFound): namespaces "bad-ns" not found`,
			wantSub: "namespace does not exist",
		},
		{
			name:    "resource not found",
			output:  `Error from server (NotFound): pods "missing-pod" not found`,
			wantSub: "resource was not found",
		},
		{
			name:    "kubectl not installed",
			output:  "executable file not found in $PATH",
			wantSub: "kubectl is not installed",
		},
		{
			name:    "command not found",
			output:  "kubectl: command not found",
			wantSub: "kubectl is not installed",
		},
		{
			name:    "invalid configuration",
			output:  "error: invalid configuration: no configuration has been provided",
			wantSub: "kubeconfig file is invalid or missing",
		},
		{
			name:    "i/o timeout",
			output:  "dial tcp 10.0.0.1:6443: i/o timeout",
			wantSub: "timed out",
		},
		{
			name:    "deadline exceeded",
			output:  "context deadline exceeded",
			wantSub: "timed out",
		},
		{
			name:    "certificate expired",
			output:  "x509: certificate has expired or is not yet valid",
			wantSub: "TLS certificate error",
		},
		{
			name:    "certificate unknown authority",
			output:  "x509: certificate signed by unknown authority",
			wantSub: "TLS certificate error",
		},
		{
			name:    "unknown error returns empty",
			output:  "something totally unknown happened",
			wantSub: "",
		},
		{
			name:    "empty output returns empty",
			output:  "",
			wantSub: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnoseKubectlError(tt.output)
			if tt.wantSub == "" {
				if got != "" {
					t.Errorf("diagnoseKubectlError(%q) = %q, want empty", tt.output, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("diagnoseKubectlError(%q) = empty, want substring %q", tt.output, tt.wantSub)
			}
			if !stringContains(got, tt.wantSub) {
				t.Errorf("diagnoseKubectlError(%q) = %q, missing substring %q", tt.output, got, tt.wantSub)
			}
		})
	}
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestParsePodsAffected(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{
			name:   "single pod deleted",
			output: `pod "my-pod" deleted` + "\n",
			want:   1,
		},
		{
			name:   "multiple pods deleted",
			output: "pod \"pod-a\" deleted\npod \"pod-b\" deleted\npod \"pod-c\" deleted\n",
			want:   3,
		},
		{
			name:   "deployment configured",
			output: `deployment.apps "my-deploy" configured` + "\n",
			want:   1,
		},
		{
			name:   "resource created",
			output: `configmap "my-cm" created` + "\n",
			want:   1,
		},
		{
			name:   "mixed actions",
			output: "pod \"p1\" deleted\ndeployment.apps \"d1\" configured\nservice \"s1\" created\n",
			want:   3,
		},
		{
			name:   "read-only output (no mutations)",
			output: "NAME   READY   STATUS\npod-a  1/1     Running\n",
			want:   0,
		},
		{
			name:   "empty output",
			output: "",
			want:   0,
		},
		{
			name:   "error output",
			output: "Error from server (NotFound): pods \"bad\" not found\n",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePodsAffected(tt.output)
			if got != tt.want {
				t.Errorf("parsePodsAffected(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// deletePodTool
// =============================================================================

func TestDeletePodTool_Success(t *testing.T) {
	mockOutput := `pod "web-abc123" deleted` + "\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "default",
		PodName:   "web-abc123",
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "deleted") {
		t.Errorf("deletePodTool() output = %q, want to contain 'deleted'", result.Output)
	}
	if !strings.Contains(result.Output, "web-abc123") {
		t.Errorf("deletePodTool() output = %q, want to contain pod name", result.Output)
	}
}

func TestDeletePodTool_WithGracePeriod(t *testing.T) {
	mockOutput := `pod "stuck-pod-xyz" deleted` + "\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace:          "production",
		PodName:            "stuck-pod-xyz",
		GracePeriodSeconds: 0,
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "deleted") {
		t.Errorf("deletePodTool() output = %q, want to contain 'deleted'", result.Output)
	}
}

func TestDeletePodTool_Failure(t *testing.T) {
	defer withMockKubectl("", fmt.Errorf(`Error from server (NotFound): pods "bad-pod" not found`))()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "default",
		PodName:   "bad-pod",
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("deletePodTool() output = %q, want ERROR on failure", result.Output)
	}
}

func TestDeletePodTool_PolicyDenied(t *testing.T) {
	defer withK8sPolicyEnforcer(newDenyK8sDestructiveEnforcer(t))()
	defer withMockKubectl("", nil)() // should not be reached

	ctx := newK8sTestContext()
	_, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "production",
		PodName:   "api-server-abc",
	})
	if err == nil {
		t.Fatal("deletePodTool() expected Go error on policy denial, got nil")
	}
	if !strings.Contains(err.Error(), "policy denied") {
		t.Errorf("deletePodTool() error = %v, want 'policy denied'", err)
	}
}

// =============================================================================
// restartDeploymentTool
// =============================================================================

func TestRestartDeploymentTool_Success(t *testing.T) {
	mockOutput := `deployment.apps "api-server" restarted` + "\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	result, err := restartDeploymentTool(ctx, RestartDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "api-server",
	})
	if err != nil {
		t.Fatalf("restartDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "api-server") {
		t.Errorf("restartDeploymentTool() output = %q, want to contain deployment name", result.Output)
	}
}

func TestRestartDeploymentTool_Failure(t *testing.T) {
	defer withMockKubectl("", fmt.Errorf(`Error from server (NotFound): deployments "missing" not found`))()

	ctx := newK8sTestContext()
	result, err := restartDeploymentTool(ctx, RestartDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "missing",
	})
	if err != nil {
		t.Fatalf("restartDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("restartDeploymentTool() output = %q, want ERROR on failure", result.Output)
	}
}

func TestRestartDeploymentTool_PolicyDenied(t *testing.T) {
	defer withK8sPolicyEnforcer(newDenyK8sDestructiveEnforcer(t))()
	defer withMockKubectl("", nil)() // should not be reached

	ctx := newK8sTestContext()
	_, err := restartDeploymentTool(ctx, RestartDeploymentArgs{
		Namespace:      "production",
		DeploymentName: "web-frontend",
	})
	if err == nil {
		t.Fatal("restartDeploymentTool() expected Go error on policy denial, got nil")
	}
	if !strings.Contains(err.Error(), "policy denied") {
		t.Errorf("restartDeploymentTool() error = %v, want 'policy denied'", err)
	}
}

// =============================================================================
// scaleDeploymentTool
// =============================================================================

func TestScaleDeploymentTool_Success(t *testing.T) {
	mockOutput := `deployment.apps "web" scaled` + "\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "web",
		Replicas:       5,
	})
	if err != nil {
		t.Fatalf("scaleDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "scaled") || !strings.Contains(result.Output, "web") {
		t.Errorf("scaleDeploymentTool() output = %q, want scaled deployment output", result.Output)
	}
}

func TestScaleDeploymentTool_ScaleToZero(t *testing.T) {
	mockOutput := `deployment.apps "batch-worker" scaled` + "\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "jobs",
		DeploymentName: "batch-worker",
		Replicas:       0,
	})
	if err != nil {
		t.Fatalf("scaleDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "scaled") {
		t.Errorf("scaleDeploymentTool() output = %q, want 'scaled'", result.Output)
	}
}

func TestScaleDeploymentTool_Failure(t *testing.T) {
	defer withMockKubectl("", fmt.Errorf(`Error from server (NotFound): deployments "ghost" not found`))()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "ghost",
		Replicas:       3,
	})
	if err != nil {
		t.Fatalf("scaleDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("scaleDeploymentTool() output = %q, want ERROR on failure", result.Output)
	}
}

func TestScaleDeploymentTool_PolicyDenied(t *testing.T) {
	defer withK8sPolicyEnforcer(newDenyK8sDestructiveEnforcer(t))()
	defer withMockKubectl("", nil)() // should not be reached

	ctx := newK8sTestContext()
	_, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "production",
		DeploymentName: "api",
		Replicas:       10,
	})
	if err == nil {
		t.Fatal("scaleDeploymentTool() expected Go error on policy denial, got nil")
	}
	if !strings.Contains(err.Error(), "policy denied") {
		t.Errorf("scaleDeploymentTool() error = %v, want 'policy denied'", err)
	}
}

// =============================================================================
// Blast-radius enforcement tests
// =============================================================================

func TestDeletePodTool_BlastRadiusAllowed(t *testing.T) {
	// Policy: allow destructive with max 2 pods; 1 pod deleted — should pass.
	defer withK8sPolicyEnforcer(newK8sBlastRadiusEnforcer(t, 2))()
	mockOutput := `pod "pod-one" deleted` + "\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "default",
		PodName:   "pod-one",
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected error with 1 pod within limit of 2: %v", err)
	}
	if !strings.Contains(result.Output, "deleted") {
		t.Errorf("deletePodTool() output = %q, want 'deleted'", result.Output)
	}
}

func TestDeletePodTool_BlastRadiusDenied(t *testing.T) {
	// Policy: allow destructive with max 1 pod.
	// Mock: returns 3 deletion confirmation lines (simulates unexpected bulk delete).
	defer withK8sPolicyEnforcer(newK8sBlastRadiusEnforcer(t, 1))()
	mockOutput := "pod \"pod-a\" deleted\npod \"pod-b\" deleted\npod \"pod-c\" deleted\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	_, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "default",
		PodName:   "pod-a",
	})
	if err == nil {
		t.Fatal("deletePodTool() expected error when blast-radius limit (1) exceeded by 3 pods")
	}
	if !strings.Contains(err.Error(), "policy denied after execution") {
		t.Errorf("deletePodTool() error = %v, want 'policy denied after execution'", err)
	}
}

// =============================================================================
// Infra.json enforcement tests
// =============================================================================

// withK8sInfraConfig temporarily sets the package-level infraConfig for k8s tests.
func withK8sInfraConfig(cfg *infra.Config) func() {
	old := infraConfig
	infraConfig = cfg
	return func() { infraConfig = old }
}

// makeK8sTestInfraConfig builds a minimal infra.Config with one registered database
// that has a K8s namespace and cluster.
func makeK8sTestInfraConfig() *infra.Config {
	return &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				Name:         "prod-db",
				K8sNamespace: "prod-namespace",
				K8sCluster:   "prod-cluster",
				Tags:         []string{"production"},
			},
		},
		K8sClusters: map[string]infra.K8sCluster{
			"prod-cluster": {
				Name:    "prod-cluster",
				Context: "gke_prod",
				Tags:    []string{"production"},
			},
		},
	}
}

func TestResolveNamespaceInfo_InfraEnforced_UnknownNamespace(t *testing.T) {
	// infraConfig is set but namespace is not registered → hard reject.
	defer withK8sInfraConfig(makeK8sTestInfraConfig())()

	_, err := resolveNamespaceInfo("unknown-namespace")
	if err == nil {
		t.Fatal("resolveNamespaceInfo() error = nil, want error for unregistered namespace with infra config set")
	}
	if !strings.Contains(err.Error(), "not registered in infrastructure config") {
		t.Errorf("resolveNamespaceInfo() error = %q, want 'not registered in infrastructure config'", err.Error())
	}
	if !strings.Contains(err.Error(), "prod-db") {
		t.Errorf("resolveNamespaceInfo() error = %q, want known database 'prod-db' listed", err.Error())
	}
}

func TestResolveNamespaceInfo_InfraEnforced_RegisteredByDBName(t *testing.T) {
	// infraConfig is set and input is a registered database name → succeed with namespace + tags.
	defer withK8sInfraConfig(makeK8sTestInfraConfig())()

	info, err := resolveNamespaceInfo("prod-db")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil for registered DB name", err)
	}
	if info.Namespace != "prod-namespace" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'prod-namespace'", info.Namespace)
	}
	if len(info.Tags) == 0 || info.Tags[0] != "production" {
		t.Errorf("resolveNamespaceInfo() Tags = %v, want ['production']", info.Tags)
	}
}

func TestResolveNamespaceInfo_InfraEnforced_RegisteredByNamespace(t *testing.T) {
	// infraConfig is set and input is the actual K8s namespace of a registered DB → allowed.
	defer withK8sInfraConfig(makeK8sTestInfraConfig())()

	info, err := resolveNamespaceInfo("prod-namespace")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil for registered K8s namespace", err)
	}
	if info.Namespace != "prod-namespace" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'prod-namespace'", info.Namespace)
	}
}

func TestResolveNamespaceInfo_InfraPermissive_UnknownNamespace(t *testing.T) {
	// infraConfig is nil (dev mode) → any namespace is allowed.
	defer withK8sInfraConfig(nil)()

	info, err := resolveNamespaceInfo("any-namespace")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil in dev mode (no infra config)", err)
	}
	if info.Namespace != "any-namespace" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'any-namespace'", info.Namespace)
	}
}

func TestGetPodsTool_InfraEnforced_Rejected(t *testing.T) {
	// infraConfig is set but namespace is not registered → tool returns error.
	defer withK8sInfraConfig(makeK8sTestInfraConfig())()
	defer withMockKubectl("", nil)() // should not be reached

	ctx := newK8sTestContext()
	_, err := getPodsTool(ctx, GetPodsArgs{
		Namespace: "unknown-namespace",
	})
	if err == nil {
		t.Fatal("getPodsTool() expected error for unregistered namespace with infra config set")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("getPodsTool() error = %v, want 'access denied'", err)
	}
	if !strings.Contains(err.Error(), "not registered in infrastructure config") {
		t.Errorf("getPodsTool() error = %v, want infra rejection message", err)
	}
}

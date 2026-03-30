package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"helpdesk/agentutil"
	"helpdesk/agentutil/retryutil"
	"helpdesk/internal/audit"
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

// withZeroVerifyConfig replaces verifyRetryConfig with a zero-delay 1-attempt
// config for the duration of the test, then restores the original on cleanup.
func withZeroVerifyConfig() func() {
	old := verifyRetryConfig
	verifyRetryConfig = retryutil.Config{MaxAttempts: 1, InitialDelay: 0, BackoffFactor: 1}
	return func() { verifyRetryConfig = old }
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

// kubectlResponse holds one (output, error) pair for a sequenced kubectl mock.
type kubectlResponse struct {
	out string
	err error
}

// withMockKubectlSequence replaces runKubectl with a mock that returns a
// different response for each successive call. The last entry in calls is
// reused for any calls beyond the slice length, so tests that don't care
// about extra calls can provide a safe default as the last element.
// Use this instead of withMockKubectl when a tool makes more than one
// kubectl call (e.g. mutation + Level-2 verification).
func withMockKubectlSequence(calls ...kubectlResponse) func() {
	orig := runKubectl
	i := 0
	runKubectl = func(_ context.Context, _ string, _ ...string) (string, error) {
		if i >= len(calls) {
			last := calls[len(calls)-1]
			return last.out, last.err
		}
		r := calls[i]
		i++
		return r.out, r.err
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
		{
			name:   "deployment restarted",
			output: `deployment.apps "my-deploy" restarted` + "\n",
			want:   1,
		},
		{
			name:   "deployment scaled",
			output: `deployment.apps "my-deploy" scaled` + "\n",
			want:   1,
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
	// Call 1: delete succeeds. Call 2: verification get-pod returns "not found" → pod is gone.
	defer withMockKubectlSequence(
		kubectlResponse{out: mockOutput, err: nil},
		kubectlResponse{out: "", err: fmt.Errorf(`kubectl failed: exit status 1\nOutput: Error from server (NotFound): pods "web-abc123" not found`)},
	)()

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
	defer withMockKubectlSequence(
		kubectlResponse{out: mockOutput, err: nil},
		kubectlResponse{out: "", err: fmt.Errorf(`kubectl failed: exit status 1\nOutput: Error from server (NotFound): pods "stuck-pod-xyz" not found`)},
	)()

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

func TestDeletePodTool_VerificationWarning_PodStillTerminating(t *testing.T) {
	// Simulates a pod that entered Terminating state but the API still shows it.
	// Level-2 verification fires because kubectl get pod returns exit 0.
	defer withZeroVerifyConfig()()
	defer withMockKubectlSequence(
		kubectlResponse{out: `pod "stuck-pod" deleted` + "\n", err: nil},                          // delete accepted
		kubectlResponse{out: "NAME      READY   STATUS\nstuck-pod 0/1     Terminating\n", err: nil}, // pod still visible
	)()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "production",
		PodName:   "stuck-pod",
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "VERIFICATION WARNING") {
		t.Errorf("deletePodTool() output = %q, want VERIFICATION WARNING", result.Output)
	}
	if !strings.Contains(result.Output, "stuck-pod") {
		t.Errorf("deletePodTool() output = %q, want pod name in warning", result.Output)
	}
	if result.VerifyStatus != "warning" {
		t.Errorf("deletePodTool() VerifyStatus = %q, want warning", result.VerifyStatus)
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
	// Call 1: rollout restart. Call 2: verification get-deployment annotations → restartedAt present.
	defer withMockKubectlSequence(
		kubectlResponse{out: mockOutput, err: nil},
		kubectlResponse{out: `map[kubectl.kubernetes.io/restartedAt:2026-03-03T10:00:00Z]`, err: nil},
	)()

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

func TestRestartDeploymentTool_VerificationWarning_AnnotationMissing(t *testing.T) {
	// Simulates a restart command that appeared to succeed but the restartedAt
	// annotation is absent from the deployment spec — Level-2 verification fires.
	defer withZeroVerifyConfig()()
	defer withMockKubectlSequence(
		kubectlResponse{out: `deployment.apps "api-server" restarted` + "\n", err: nil},
		kubectlResponse{out: `map[]`, err: nil}, // annotations map is empty
	)()

	ctx := newK8sTestContext()
	result, err := restartDeploymentTool(ctx, RestartDeploymentArgs{
		Namespace:      "staging",
		DeploymentName: "api-server",
	})
	if err != nil {
		t.Fatalf("restartDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "VERIFICATION WARNING") {
		t.Errorf("restartDeploymentTool() output = %q, want VERIFICATION WARNING", result.Output)
	}
	if !strings.Contains(result.Output, "api-server") {
		t.Errorf("restartDeploymentTool() output = %q, want deployment name in warning", result.Output)
	}
	if result.VerifyStatus != "warning" {
		t.Errorf("restartDeploymentTool() VerifyStatus = %q, want warning", result.VerifyStatus)
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
	// Call 1: pre-state read (current replicas before scale).
	// Call 2: scale. Call 3: verification get spec.replicas → matches requested 5.
	defer withMockKubectlSequence(
		kubectlResponse{out: "3", err: nil},
		kubectlResponse{out: mockOutput, err: nil},
		kubectlResponse{out: "5", err: nil},
	)()

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
	// Call 1: pre-state read (current replicas before scale).
	// Call 2: scale to 0. Call 3: verification get spec.replicas → "0".
	defer withMockKubectlSequence(
		kubectlResponse{out: "2", err: nil},
		kubectlResponse{out: mockOutput, err: nil},
		kubectlResponse{out: "0", err: nil},
	)()

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

func TestScaleDeploymentTool_CapturesPreState(t *testing.T) {
	// Verifies that scaleDeploymentImpl reads the current replica count before
	// scaling and stores it as PreState in the audit event. Uses a real
	// ToolAuditor backed by an in-process audit store.
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "k8s_prestate_test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	defer withZeroVerifyConfig()()

	origAuditor := toolAuditor
	toolAuditor = audit.NewToolAuditor(store, "k8s_agent", "sess_prestate", "trace_prestate")
	defer func() { toolAuditor = origAuditor }()

	defer withMockKubectlSequence(
		kubectlResponse{out: "3", err: nil},                                    // pre-state read → previous = 3
		kubectlResponse{out: `deployment.apps "web" scaled` + "\n", err: nil}, // scale
		kubectlResponse{out: "5", err: nil},                                    // verify
	)()

	ctx := newK8sTestContext()
	_, err = scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "web",
		Replicas:       5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, queryErr := store.Query(context.Background(), audit.QueryOptions{
		ToolName:  "scale_deployment",
		EventType: audit.EventTypeToolExecution,
	})
	if queryErr != nil {
		t.Fatalf("Query: %v", queryErr)
	}
	if len(events) == 0 {
		t.Fatal("no scale_deployment audit event found")
	}
	ev := events[0]
	if ev.Tool == nil || len(ev.Tool.PreState) == 0 {
		t.Fatal("PreState is empty in audit event")
	}
	var pre audit.ScalePreState
	if jsonErr := json.Unmarshal(ev.Tool.PreState, &pre); jsonErr != nil {
		t.Fatalf("unmarshal PreState: %v", jsonErr)
	}
	if pre.PreviousReplicas != 3 {
		t.Errorf("PreviousReplicas = %d, want 3", pre.PreviousReplicas)
	}
	if pre.DeploymentName != "web" {
		t.Errorf("DeploymentName = %q, want web", pre.DeploymentName)
	}
}

func TestScaleDeploymentTool_PreStateReadFailure_ToolStillRuns(t *testing.T) {
	// If the pre-state kubectl read fails, the scale must still proceed.
	defer withMockKubectlSequence(
		kubectlResponse{out: "", err: fmt.Errorf("connection refused")},        // pre-state read fails
		kubectlResponse{out: `deployment.apps "web" scaled` + "\n", err: nil}, // scale
		kubectlResponse{out: "2", err: nil},                                    // verify
	)()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "web",
		Replicas:       2,
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "scaled") {
		t.Errorf("output = %q, want 'scaled'", result.Output)
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

func TestScaleDeploymentTool_VerificationFailed_WrongReplicas(t *testing.T) {
	// Simulates the kubectl scale command reporting success but spec.replicas
	// not matching the requested count — Level-2 verification fires.
	defer withZeroVerifyConfig()()
	defer withMockKubectlSequence(
		kubectlResponse{out: `deployment.apps "web" scaled` + "\n", err: nil},
		kubectlResponse{out: "3", err: nil}, // actual replicas is 3, not the requested 5
	)()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "production",
		DeploymentName: "web",
		Replicas:       5,
	})
	if err != nil {
		t.Fatalf("scaleDeploymentTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "VERIFICATION FAILED") {
		t.Errorf("scaleDeploymentTool() output = %q, want VERIFICATION FAILED", result.Output)
	}
	if !strings.Contains(result.Output, "web") {
		t.Errorf("scaleDeploymentTool() output = %q, want deployment name in output", result.Output)
	}
	if result.VerifyStatus != "failed" {
		t.Errorf("scaleDeploymentTool() VerifyStatus = %q, want failed", result.VerifyStatus)
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
// Retry scenario tests — Level-2 resolves after 1 or more re-checks
// =============================================================================

func TestDeletePodTool_VerificationWarning_ResolvesOnRetry(t *testing.T) {
	// Pod is still Terminating on first verify but gone by second check.
	// Tool should return ok with RetryCount=1.
	//   call #1: delete (ok)
	//   call #2: verify → pod still visible (exit 0) → not resolved
	//   call #3: verify → not found (err != nil) → resolved
	defer withMockKubectlSequence(
		kubectlResponse{out: `pod "stuck-pod" deleted` + "\n", err: nil},
		kubectlResponse{out: "NAME      READY   STATUS\nstuck-pod 0/1     Terminating\n", err: nil},
		kubectlResponse{out: "", err: fmt.Errorf("not found")},
	)()
	old := verifyRetryConfig
	verifyRetryConfig = retryutil.Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	defer func() { verifyRetryConfig = old }()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "production",
		PodName:   "stuck-pod",
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected Go error: %v", err)
	}
	if result.VerifyStatus != "ok" {
		t.Errorf("deletePodTool() VerifyStatus = %q, want ok", result.VerifyStatus)
	}
	if result.RetryCount != 1 {
		t.Errorf("deletePodTool() RetryCount = %d, want 1", result.RetryCount)
	}
	if strings.Contains(result.Output, "VERIFICATION WARNING") {
		t.Errorf("deletePodTool() output contains unexpected VERIFICATION WARNING when resolved")
	}
}

func TestDeletePodTool_VerificationWarning_ExhaustedEscalation(t *testing.T) {
	// Pod stays Terminating for all verify attempts; escalation guidance surfaced.
	//   call #1: delete (ok)
	//   call #2-4: verify → pod always visible (reused)
	defer withMockKubectlSequence(
		kubectlResponse{out: `pod "stuck-pod" deleted` + "\n", err: nil},
		kubectlResponse{out: "NAME      READY   STATUS\nstuck-pod 0/1     Terminating\n", err: nil},
	)()
	old := verifyRetryConfig
	verifyRetryConfig = retryutil.Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	defer func() { verifyRetryConfig = old }()

	ctx := newK8sTestContext()
	result, err := deletePodTool(ctx, DeletePodArgs{
		Namespace: "production",
		PodName:   "stuck-pod",
	})
	if err != nil {
		t.Fatalf("deletePodTool() unexpected Go error: %v", err)
	}
	if result.VerifyStatus != "warning" {
		t.Errorf("deletePodTool() VerifyStatus = %q, want warning", result.VerifyStatus)
	}
	if result.RetryCount != 2 {
		t.Errorf("deletePodTool() RetryCount = %d, want 2", result.RetryCount)
	}
	if !strings.Contains(result.Output, "--force") {
		t.Errorf("deletePodTool() output = %q, want force-delete escalation guidance", result.Output)
	}
}

func TestRestartDeploymentTool_VerificationWarning_ResolvesOnRetry(t *testing.T) {
	// First verify shows no restartedAt annotation; second confirms it's present.
	// Tool should return ok with RetryCount=1.
	//   call #1: rollout restart (ok)
	//   call #2: verify → map[] (no annotation)
	//   call #3: verify → restartedAt present → resolved
	defer withMockKubectlSequence(
		kubectlResponse{out: `deployment.apps "api-server" restarted` + "\n", err: nil},
		kubectlResponse{out: `map[]`, err: nil},
		kubectlResponse{out: `map[kubectl.kubernetes.io/restartedAt:2026-03-06T00:00:00Z]`, err: nil},
	)()
	old := verifyRetryConfig
	verifyRetryConfig = retryutil.Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	defer func() { verifyRetryConfig = old }()

	ctx := newK8sTestContext()
	result, err := restartDeploymentTool(ctx, RestartDeploymentArgs{
		Namespace:      "staging",
		DeploymentName: "api-server",
	})
	if err != nil {
		t.Fatalf("restartDeploymentTool() unexpected Go error: %v", err)
	}
	if result.VerifyStatus != "ok" {
		t.Errorf("restartDeploymentTool() VerifyStatus = %q, want ok", result.VerifyStatus)
	}
	if result.RetryCount != 1 {
		t.Errorf("restartDeploymentTool() RetryCount = %d, want 1", result.RetryCount)
	}
	if strings.Contains(result.Output, "VERIFICATION WARNING") {
		t.Errorf("restartDeploymentTool() output contains unexpected VERIFICATION WARNING when resolved")
	}
}

func TestScaleDeploymentTool_Level2_RetryApplySucceeds(t *testing.T) {
	// First verify shows wrong replicas; scale re-applied; second verify confirms.
	// Tool should return ok with RetryCount=1.
	//   call #1: scale (ok)
	//   call #2: verify → "3" (wrong) → re-apply scale → call #3 consumed by re-apply
	//   call #4: verify → "5" (correct) → resolved
	defer withMockKubectlSequence(
		kubectlResponse{out: `deployment.apps "web" scaled` + "\n", err: nil}, // initial scale
		kubectlResponse{out: "3", err: nil},                                    // verify #1: wrong
		kubectlResponse{out: `deployment.apps "web" scaled` + "\n", err: nil}, // re-apply
		kubectlResponse{out: "5", err: nil},                                    // verify #2: correct
	)()
	old := verifyRetryConfig
	verifyRetryConfig = retryutil.Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	defer func() { verifyRetryConfig = old }()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "production",
		DeploymentName: "web",
		Replicas:       5,
	})
	if err != nil {
		t.Fatalf("scaleDeploymentTool() unexpected Go error: %v", err)
	}
	if result.VerifyStatus != "ok" {
		t.Errorf("scaleDeploymentTool() VerifyStatus = %q, want ok", result.VerifyStatus)
	}
	if result.RetryCount != 1 {
		t.Errorf("scaleDeploymentTool() RetryCount = %d, want 1", result.RetryCount)
	}
}

func TestScaleDeploymentTool_Level2_RetryApplyFails(t *testing.T) {
	// All verify calls return wrong replicas — failed after all re-apply attempts.
	//   call #1: scale (ok)
	//   call #2: verify → "3" (wrong) → re-apply (call #3)
	//   call #4: verify → "3" (wrong) → re-apply (call #5)
	//   call #6: verify → "3" (wrong) → done (last attempt, no re-apply)
	defer withMockKubectlSequence(
		kubectlResponse{out: `deployment.apps "web" scaled` + "\n", err: nil},
		kubectlResponse{out: "3", err: nil}, // always returns 3 (reused)
	)()
	old := verifyRetryConfig
	verifyRetryConfig = retryutil.Config{MaxAttempts: 3, InitialDelay: 0, BackoffFactor: 1}
	defer func() { verifyRetryConfig = old }()

	ctx := newK8sTestContext()
	result, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "production",
		DeploymentName: "web",
		Replicas:       5,
	})
	if err != nil {
		t.Fatalf("scaleDeploymentTool() unexpected Go error: %v", err)
	}
	if result.VerifyStatus != "failed" {
		t.Errorf("scaleDeploymentTool() VerifyStatus = %q, want failed", result.VerifyStatus)
	}
	if result.RetryCount != 2 {
		t.Errorf("scaleDeploymentTool() RetryCount = %d, want 2", result.RetryCount)
	}
	if !strings.Contains(result.Output, "VERIFICATION FAILED") {
		t.Errorf("scaleDeploymentTool() output = %q, want 'VERIFICATION FAILED'", result.Output)
	}
}

// =============================================================================
// Blast-radius enforcement tests
// =============================================================================

func TestDeletePodTool_BlastRadiusAllowed(t *testing.T) {
	// Policy: allow destructive with max 2 pods; 1 pod deleted — should pass.
	defer withK8sPolicyEnforcer(newK8sBlastRadiusEnforcer(t, 2))()
	mockOutput := `pod "pod-one" deleted` + "\n"
	defer withMockKubectlSequence(
		kubectlResponse{out: mockOutput, err: nil},
		kubectlResponse{out: "", err: fmt.Errorf(`kubectl failed: exit status 1\nOutput: Error from server (NotFound): pods "pod-one" not found`)},
	)()

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

func TestScaleDeploymentTool_BlastRadiusDenied_PreExec(t *testing.T) {
	// Policy: allow destructive with max 5 pods. scale to 20 → denied pre-exec.
	defer withK8sPolicyEnforcer(newK8sBlastRadiusEnforcer(t, 5))()
	// kubectl must never be called — the pre-execution blast-radius check fires first.
	kubectlCalled := false
	orig := runKubectl
	runKubectl = func(ctx context.Context, kubeContext string, args ...string) (string, error) {
		kubectlCalled = true
		return "", nil
	}
	defer func() { runKubectl = orig }()

	ctx := newK8sTestContext()
	_, err := scaleDeploymentTool(ctx, ScaleDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "web",
		Replicas:       20,
	})
	if err == nil {
		t.Fatal("scaleDeploymentTool() expected error when blast-radius pre-exec check (20 > 5) fires")
	}
	if !strings.Contains(err.Error(), "blast radius check denied") {
		t.Errorf("scaleDeploymentTool() error = %v, want 'blast radius check denied'", err)
	}
	if kubectlCalled {
		t.Error("kubectl was called despite pre-execution blast-radius denial — check should block before execution")
	}
}

func TestRestartDeploymentTool_BlastRadiusEnforced_PostExec(t *testing.T) {
	// After the parsePodsAffected fix, " restarted" suffix is recognised.
	// Policy: allow up to 1 resource; mock returns 2 "restarted" lines → post-exec
	// check fires with count=2 which exceeds the limit.
	defer withK8sPolicyEnforcer(newK8sBlastRadiusEnforcer(t, 1))()
	// Simulate two deployments restarted in one rollout-restart command.
	mockOutput := "deployment.apps \"api\" restarted\ndeployment.apps \"worker\" restarted\n"
	defer withMockKubectl(mockOutput, nil)()

	ctx := newK8sTestContext()
	_, err := restartDeploymentTool(ctx, RestartDeploymentArgs{
		Namespace:      "default",
		DeploymentName: "api",
	})
	if err == nil {
		t.Fatal("restartDeploymentTool() expected error when blast-radius post-exec check fires with count=2 > limit=1")
	}
	if !strings.Contains(err.Error(), "policy denied after execution") {
		t.Errorf("restartDeploymentTool() error = %v, want 'policy denied after execution'", err)
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

func TestResolveNamespaceInfo_InfraEnforced_UnknownNamespaceUnknownContext(t *testing.T) {
	// infraConfig is set, namespace is not a DB namespace, and context doesn't match
	// any registered cluster → hard reject (can't determine which cluster this belongs to).
	cfg := makeK8sTestInfraConfig()
	// Add a second cluster so the sole-cluster fallback doesn't apply.
	cfg.K8sClusters["other-cluster"] = infra.K8sCluster{Name: "other", Context: "gke_staging", Tags: []string{"staging"}}
	defer withK8sInfraConfig(cfg)()

	_, err := resolveNamespaceInfo("unknown-namespace", "unknown-context")
	if err == nil {
		t.Fatal("resolveNamespaceInfo() error = nil, want error for namespace in unrecognized context with infra config set")
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

	info, err := resolveNamespaceInfo("prod-db", "")
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

	info, err := resolveNamespaceInfo("prod-namespace", "")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil for registered K8s namespace", err)
	}
	if info.Namespace != "prod-namespace" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'prod-namespace'", info.Namespace)
	}
}

func TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceInKnownCluster(t *testing.T) {
	// infraConfig is set and namespace is not a DB namespace, but the context matches
	// a registered cluster → succeed with cluster-level tags (e.g. "default" namespace
	// on a cluster tagged "development" should inherit those tags).
	defer withK8sInfraConfig(makeK8sTestInfraConfig())()

	info, err := resolveNamespaceInfo("default", "gke_prod")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil for non-DB namespace in registered cluster", err)
	}
	if info.Namespace != "default" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'default'", info.Namespace)
	}
	if len(info.Tags) == 0 || info.Tags[0] != "production" {
		t.Errorf("resolveNamespaceInfo() Tags = %v, want cluster tags ['production']", info.Tags)
	}
}

func TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceNoContextSoleCluster(t *testing.T) {
	// infraConfig has exactly one cluster; no context passed → use sole cluster's tags.
	defer withK8sInfraConfig(makeK8sTestInfraConfig())()

	info, err := resolveNamespaceInfo("default", "")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil for non-DB namespace with sole cluster default", err)
	}
	if info.Namespace != "default" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'default'", info.Namespace)
	}
	if len(info.Tags) == 0 || info.Tags[0] != "production" {
		t.Errorf("resolveNamespaceInfo() Tags = %v, want sole cluster tags ['production']", info.Tags)
	}
}

func TestResolveNamespaceInfo_InfraPermissive_UnknownNamespace(t *testing.T) {
	// infraConfig is nil (dev mode) → any namespace is allowed.
	defer withK8sInfraConfig(nil)()

	info, err := resolveNamespaceInfo("any-namespace", "")
	if err != nil {
		t.Fatalf("resolveNamespaceInfo() error = %v, want nil in dev mode (no infra config)", err)
	}
	if info.Namespace != "any-namespace" {
		t.Errorf("resolveNamespaceInfo() Namespace = %q, want 'any-namespace'", info.Namespace)
	}
}

func TestGetPodsTool_InfraEnforced_Rejected(t *testing.T) {
	// infraConfig is set with multiple clusters; namespace is not registered and
	// context doesn't match any cluster → tool returns access denied.
	cfg := makeK8sTestInfraConfig()
	cfg.K8sClusters["other-cluster"] = infra.K8sCluster{Name: "other", Context: "gke_staging", Tags: []string{"staging"}}
	defer withK8sInfraConfig(cfg)()
	defer withMockKubectl("", nil)() // should not be reached

	ctx := newK8sTestContext()
	_, err := getPodsTool(ctx, GetPodsArgs{
		Namespace: "unknown-namespace",
		Context:   "unknown-context",
	})
	if err == nil {
		t.Fatal("getPodsTool() expected error for namespace in unrecognized context with infra config set")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("getPodsTool() error = %v, want 'access denied'", err)
	}
	if !strings.Contains(err.Error(), "not registered in infrastructure config") {
		t.Errorf("getPodsTool() error = %v, want infra rejection message", err)
	}
}

// --- get_pod_resources tests ---

// injectFakeClientset injects cs under the given context key and returns cleanup.
func injectFakeClientset(kubeContext string, cs *fake.Clientset) func() {
	return sharedClient.injectForTest(kubeContext, cs)
}

func TestGetPodResources_RequestsLimitsOnly(t *testing.T) {
	// Set up a fake pod with requests and limits, no metrics-server.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc123", Namespace: "production"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(pod)
	defer injectFakeClientset("", cs)()
	// kubectl top fails (metrics-server absent)
	defer withMockKubectl("", fmt.Errorf("metrics not available"))()

	ctx := newK8sTestContext()
	result, err := getPodResourcesTool(ctx, GetPodResourcesArgs{Namespace: "production"})
	if err != nil {
		t.Fatalf("getPodResourcesTool() error = %v", err)
	}
	if result.Count != 1 {
		t.Errorf("Count = %d, want 1", result.Count)
	}
	c := result.Containers[0]
	if c.PodName != "web-abc123" {
		t.Errorf("PodName = %q, want web-abc123", c.PodName)
	}
	if c.ContainerName != "app" {
		t.Errorf("ContainerName = %q, want app", c.ContainerName)
	}
	if c.CPURequest != "250m" {
		t.Errorf("CPURequest = %q, want 250m", c.CPURequest)
	}
	if c.MemLimit != "1Gi" {
		t.Errorf("MemLimit = %q, want 1Gi", c.MemLimit)
	}
	if c.CPUUsage != "" || c.MemUsage != "" {
		t.Errorf("expected empty live usage when metrics unavailable, got CPU=%q Mem=%q", c.CPUUsage, c.MemUsage)
	}
	if result.MetricsNote == "" {
		t.Error("expected MetricsNote when metrics unavailable, got empty string")
	}
}

func TestGetPodResources_WithLiveUsage(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-xyz", Namespace: "staging"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "server"}},
		},
	}
	cs := fake.NewSimpleClientset(pod)
	defer injectFakeClientset("", cs)()
	// kubectl top output: NAME  CPU(cores)  MEMORY(bytes)
	defer withMockKubectl("api-xyz   120m   200Mi", nil)()

	ctx := newK8sTestContext()
	result, err := getPodResourcesTool(ctx, GetPodResourcesArgs{Namespace: "staging"})
	if err != nil {
		t.Fatalf("getPodResourcesTool() error = %v", err)
	}
	if result.MetricsNote != "" {
		t.Errorf("unexpected MetricsNote = %q", result.MetricsNote)
	}
	if result.Containers[0].CPUUsage != "120m" {
		t.Errorf("CPUUsage = %q, want 120m", result.Containers[0].CPUUsage)
	}
	if result.Containers[0].MemUsage != "200Mi" {
		t.Errorf("MemUsage = %q, want 200Mi", result.Containers[0].MemUsage)
	}
}

func TestGetPodResources_PolicyDenied(t *testing.T) {
	defer withK8sPolicyEnforcer(newDenyK8sDestructiveEnforcer(t))()
	// Policy only denies destructive — read should still pass.
	// Use a fake client so the tool can actually execute.
	cs := fake.NewSimpleClientset()
	defer injectFakeClientset("", cs)()
	defer withMockKubectl("", fmt.Errorf("no metrics"))()

	ctx := newK8sTestContext()
	result, err := getPodResourcesTool(ctx, GetPodResourcesArgs{Namespace: "production"})
	if err != nil {
		t.Fatalf("getPodResourcesTool() unexpected error = %v", err)
	}
	if result.Count != 0 {
		t.Errorf("Count = %d, want 0 (no pods)", result.Count)
	}
}

// --- get_node_status tests ---

func TestGetNodeStatus_AllNodes(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"node-role.kubernetes.io/worker": ""},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3800m"),
				corev1.ResourceMemory: resource.MustParse("7Gi"),
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.32.0"},
		},
	}
	cs := fake.NewSimpleClientset(node)
	defer injectFakeClientset("", cs)()

	ctx := newK8sTestContext()
	result, err := getNodeStatusTool(ctx, GetNodeStatusArgs{})
	if err != nil {
		t.Fatalf("getNodeStatusTool() error = %v", err)
	}
	if result.Count != 1 {
		t.Errorf("Count = %d, want 1", result.Count)
	}
	n := result.Nodes[0]
	if n.Name != "node-1" {
		t.Errorf("Name = %q, want node-1", n.Name)
	}
	if n.Status != "Ready" {
		t.Errorf("Status = %q, want Ready", n.Status)
	}
	if n.AllocatableCPU != "3800m" {
		t.Errorf("AllocatableCPU = %q, want 3800m", n.AllocatableCPU)
	}
	if n.CapacityMem != "8Gi" {
		t.Errorf("CapacityMem = %q, want 8Gi", n.CapacityMem)
	}
	if n.KubeletVersion != "v1.32.0" {
		t.Errorf("KubeletVersion = %q, want v1.32.0", n.KubeletVersion)
	}
	if len(n.Conditions) != 3 {
		t.Errorf("Conditions len = %d, want 3", len(n.Conditions))
	}
}

func TestGetNodeStatus_SingleNode(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "control-plane-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
	}
	cs := fake.NewSimpleClientset(node)
	defer injectFakeClientset("", cs)()

	ctx := newK8sTestContext()
	result, err := getNodeStatusTool(ctx, GetNodeStatusArgs{NodeName: "control-plane-1"})
	if err != nil {
		t.Fatalf("getNodeStatusTool() error = %v", err)
	}
	if result.Count != 1 {
		t.Errorf("Count = %d, want 1", result.Count)
	}
	if result.Nodes[0].Name != "control-plane-1" {
		t.Errorf("Name = %q, want control-plane-1", result.Nodes[0].Name)
	}
}

func TestGetNodeStatus_MemoryPressure_IncludesMessage(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-degraded"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Message: "kubelet stopped posting status"},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue, Message: "memory usage above threshold"},
			},
		},
	}
	cs := fake.NewSimpleClientset(node)
	defer injectFakeClientset("", cs)()

	ctx := newK8sTestContext()
	result, err := getNodeStatusTool(ctx, GetNodeStatusArgs{})
	if err != nil {
		t.Fatalf("getNodeStatusTool() error = %v", err)
	}
	n := result.Nodes[0]
	// Find the MemoryPressure condition and verify message is present
	var memCond *NodeCondition
	for i := range n.Conditions {
		if n.Conditions[i].Type == "MemoryPressure" {
			memCond = &n.Conditions[i]
			break
		}
	}
	if memCond == nil {
		t.Fatal("MemoryPressure condition not found")
	}
	if memCond.Message == "" {
		t.Error("MemoryPressure condition should have Message set when Status=True")
	}
}

// Package helm_test validates Helm chart template rendering.
//
// These tests exercise the Kubernetes deployment topology produced by
// `helm template`, catching bugs that Go unit tests and docker-compose-based
// integration tests cannot: incorrect env var propagation, wrong volume mounts,
// missing RBAC resources, and component wiring mistakes.
//
// Requirements: `helm` must be in PATH. Tests skip automatically if it is not.
// No Kubernetes cluster is needed — only `helm template` (dry-run rendering).
//
// Run with: go test ./testing/helm/...
package helm_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// chartPath returns the absolute path to the Helm chart directory.
func chartPath(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../helpdesk/testing/helm/helm_test.go
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "deploy", "helm", "helpdesk")
}

// render runs `helm template test <chart>` with the given --set overrides and
// returns all rendered Kubernetes objects indexed by "Kind/name".
func render(t *testing.T, setFlags ...string) map[string]map[string]any {
	t.Helper()

	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not in PATH; skipping Helm chart tests")
	}

	args := []string{"template", "test", chartPath(t)}
	for _, f := range setFlags {
		args = append(args, "--set", f)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return parseManifests(t, string(out))
}

// parseManifests splits multi-document YAML produced by `helm template` and
// returns objects indexed by "Kind/name".
func parseManifests(t *testing.T, data string) map[string]map[string]any {
	t.Helper()
	result := make(map[string]map[string]any)

	for _, doc := range strings.Split(data, "\n---") {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}
		kind, _ := obj["kind"].(string)
		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if kind != "" && name != "" {
			result[kind+"/"+name] = obj
		}
	}
	return result
}

// containerByName finds a container spec within a Deployment by container name.
func containerByName(t *testing.T, deployment map[string]any, containerName string) map[string]any {
	t.Helper()
	spec := deployment["spec"].(map[string]any)
	tmpl := spec["template"].(map[string]any)
	podSpec := tmpl["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	for _, c := range containers {
		container, _ := c.(map[string]any)
		if container["name"] == containerName {
			return container
		}
	}
	t.Fatalf("container %q not found in deployment %q", containerName, deployment["metadata"].(map[string]any)["name"])
	return nil
}

// containerArgs returns the args slice of a container.
func containerArgs(container map[string]any) []string {
	raw, _ := container["args"].([]any)
	result := make([]string, len(raw))
	for i, v := range raw {
		result[i], _ = v.(string)
	}
	return result
}

// containerEnvMap returns name→value for a container's env vars.
// Entries backed by secretKeyRef (no plain value) are omitted.
func containerEnvMap(container map[string]any) map[string]string {
	env, _ := container["env"].([]any)
	result := make(map[string]string)
	for _, e := range env {
		entry, _ := e.(map[string]any)
		name, _ := entry["name"].(string)
		value, _ := entry["value"].(string)
		if name != "" {
			result[name] = value
		}
	}
	return result
}

// hasArg returns true if any element of args has the given prefix.
func hasArg(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// hasVolumeMounts returns true if the container has any volumeMounts.
func hasVolumeMounts(container map[string]any) bool {
	mounts, _ := container["volumeMounts"].([]any)
	return len(mounts) > 0
}

// ─────────────────────────────────────────────────────────────────────────────
// Auditor: socket vs. HTTP polling mode
// ─────────────────────────────────────────────────────────────────────────────

// TestAuditorSocketMode verifies that with persistence enabled, auditor uses
// -socket=... and mounts the PVC volume.
func TestAuditorSocketMode(t *testing.T) {
	objects := render(t,
		"governance.auditor.enabled=true",
		"governance.auditd.persistence.enabled=true",
	)
	dep, ok := objects["Deployment/test-auditor"]
	if !ok {
		t.Fatal("Deployment/test-auditor not found")
	}
	c := containerByName(t, dep, "auditor")
	args := containerArgs(c)

	if !hasArg(args, "-socket=") {
		t.Errorf("expected -socket=... arg when persistence=true; got: %v", args)
	}
	if hasArg(args, "-audit-service=") {
		t.Errorf("unexpected -audit-service=... arg when persistence=true; got: %v", args)
	}
	if !hasVolumeMounts(c) {
		t.Error("expected volumeMounts when persistence=true (PVC needs to be mounted)")
	}
}

// TestAuditorHTTPPollingMode verifies that with persistence disabled, auditor
// uses -audit-service=... (HTTP polling) and has NO volume mounts.
//
// Regression test for: emptyDir volumes are per-pod and cannot be shared
// across pods — the auditor cannot read the Unix socket from auditd's emptyDir.
func TestAuditorHTTPPollingMode(t *testing.T) {
	objects := render(t,
		"governance.auditor.enabled=true",
		"governance.auditd.persistence.enabled=false",
	)
	dep, ok := objects["Deployment/test-auditor"]
	if !ok {
		t.Fatal("Deployment/test-auditor not found")
	}
	c := containerByName(t, dep, "auditor")
	args := containerArgs(c)

	if hasArg(args, "-socket=") {
		t.Errorf("unexpected -socket=... arg when persistence=false: "+
			"emptyDir is per-pod and cannot be shared across pods; got: %v", args)
	}
	if !hasArg(args, "-audit-service=") {
		t.Errorf("expected -audit-service=... arg when persistence=false; got: %v", args)
	}
	if hasVolumeMounts(c) {
		t.Error("unexpected volumeMounts when persistence=false: " +
			"no volume needed in HTTP polling mode")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Secbot: socket vs. HTTP polling mode
// ─────────────────────────────────────────────────────────────────────────────

// TestSecbotSocketMode mirrors TestAuditorSocketMode for secbot.
func TestSecbotSocketMode(t *testing.T) {
	objects := render(t,
		"governance.secbot.enabled=true",
		"governance.auditd.persistence.enabled=true",
	)
	dep, ok := objects["Deployment/test-secbot"]
	if !ok {
		t.Fatal("Deployment/test-secbot not found")
	}
	c := containerByName(t, dep, "secbot")
	args := containerArgs(c)

	if !hasArg(args, "-socket=") {
		t.Errorf("expected -socket=... arg when persistence=true; got: %v", args)
	}
	if hasArg(args, "-audit-service=") {
		t.Errorf("unexpected -audit-service=... arg when persistence=true; got: %v", args)
	}
	if !hasVolumeMounts(c) {
		t.Error("expected volumeMounts when persistence=true")
	}
}

// TestSecbotHTTPPollingMode mirrors TestAuditorHTTPPollingMode for secbot.
func TestSecbotHTTPPollingMode(t *testing.T) {
	objects := render(t,
		"governance.secbot.enabled=true",
		"governance.auditd.persistence.enabled=false",
	)
	dep, ok := objects["Deployment/test-secbot"]
	if !ok {
		t.Fatal("Deployment/test-secbot not found")
	}
	c := containerByName(t, dep, "secbot")
	args := containerArgs(c)

	if hasArg(args, "-socket=") {
		t.Errorf("unexpected -socket=... arg when persistence=false: "+
			"emptyDir cannot be shared across pods; got: %v", args)
	}
	if !hasArg(args, "-audit-service=") {
		t.Errorf("expected -audit-service=... arg when persistence=false; got: %v", args)
	}
	if hasVolumeMounts(c) {
		t.Error("unexpected volumeMounts when persistence=false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Governance env var propagation
// ─────────────────────────────────────────────────────────────────────────────

// TestAuditEnvVarsPropagate verifies that when auditd is enabled every agent
// and the orchestrator receive HELPDESK_AUDIT_ENABLED and HELPDESK_AUDIT_URL.
// Missing these causes startup failures in fix mode.
func TestAuditEnvVarsPropagate(t *testing.T) {
	objects := render(t, "governance.auditd.enabled=true")

	components := []struct{ deployment, container string }{
		{"Deployment/test-database-agent", "database-agent"},
		{"Deployment/test-k8s-agent", "k8s-agent"},
		{"Deployment/test-incident-agent", "incident-agent"},
		{"Deployment/test-research-agent", "research-agent"},
		{"Deployment/test-orchestrator", "orchestrator"},
	}

	for _, tc := range components {
		t.Run(tc.container, func(t *testing.T) {
			dep, ok := objects[tc.deployment]
			if !ok {
				t.Fatalf("Deployment %q not found", tc.deployment)
			}
			env := containerEnvMap(containerByName(t, dep, tc.container))

			if env["HELPDESK_AUDIT_ENABLED"] != "true" {
				t.Errorf("HELPDESK_AUDIT_ENABLED: want \"true\", got %q", env["HELPDESK_AUDIT_ENABLED"])
			}
			if env["HELPDESK_AUDIT_URL"] == "" {
				t.Error("HELPDESK_AUDIT_URL is empty")
			}
		})
	}
}

// TestPolicyEnvVarsPropagate verifies that when policy is enabled all agents
// receive HELPDESK_POLICY_ENABLED and HELPDESK_POLICY_FILE.
func TestPolicyEnvVarsPropagate(t *testing.T) {
	objects := render(t,
		"governance.policy.enabled=true",
		"governance.policy.configMap=my-policies",
	)

	components := []struct{ deployment, container string }{
		{"Deployment/test-database-agent", "database-agent"},
		{"Deployment/test-k8s-agent", "k8s-agent"},
		{"Deployment/test-incident-agent", "incident-agent"},
		{"Deployment/test-research-agent", "research-agent"},
	}

	for _, tc := range components {
		t.Run(tc.container, func(t *testing.T) {
			dep, ok := objects[tc.deployment]
			if !ok {
				t.Fatalf("Deployment %q not found", tc.deployment)
			}
			env := containerEnvMap(containerByName(t, dep, tc.container))

			if env["HELPDESK_POLICY_ENABLED"] != "true" {
				t.Errorf("HELPDESK_POLICY_ENABLED: want \"true\", got %q", env["HELPDESK_POLICY_ENABLED"])
			}
			if env["HELPDESK_POLICY_FILE"] == "" {
				t.Error("HELPDESK_POLICY_FILE is empty")
			}
		})
	}
}

// TestOperatingModePropagate verifies that governance.operatingMode is
// propagated to all agents and the orchestrator.
func TestOperatingModePropagate(t *testing.T) {
	objects := render(t,
		"governance.operatingMode=fix",
		"governance.auditd.enabled=true",
		"governance.policy.enabled=true",
		"governance.policy.configMap=my-policies",
	)

	components := []struct{ deployment, container string }{
		{"Deployment/test-database-agent", "database-agent"},
		{"Deployment/test-k8s-agent", "k8s-agent"},
		{"Deployment/test-incident-agent", "incident-agent"},
		{"Deployment/test-research-agent", "research-agent"},
		{"Deployment/test-orchestrator", "orchestrator"},
	}

	for _, tc := range components {
		t.Run(tc.container, func(t *testing.T) {
			dep, ok := objects[tc.deployment]
			if !ok {
				t.Fatalf("Deployment %q not found", tc.deployment)
			}
			env := containerEnvMap(containerByName(t, dep, tc.container))

			if env["HELPDESK_OPERATING_MODE"] != "fix" {
				t.Errorf("HELPDESK_OPERATING_MODE: want \"fix\", got %q", env["HELPDESK_OPERATING_MODE"])
			}
		})
	}
}

// TestOperatingModeAbsentByDefault verifies that HELPDESK_OPERATING_MODE is
// NOT set when governance.operatingMode is empty (the default).
func TestOperatingModeAbsentByDefault(t *testing.T) {
	objects := render(t) // all defaults

	components := []struct{ deployment, container string }{
		{"Deployment/test-database-agent", "database-agent"},
		{"Deployment/test-k8s-agent", "k8s-agent"},
		{"Deployment/test-orchestrator", "orchestrator"},
	}

	for _, tc := range components {
		t.Run(tc.container, func(t *testing.T) {
			dep, ok := objects[tc.deployment]
			if !ok {
				t.Fatalf("Deployment %q not found", tc.deployment)
			}
			env := containerEnvMap(containerByName(t, dep, tc.container))
			if v, set := env["HELPDESK_OPERATING_MODE"]; set && v != "" {
				t.Errorf("HELPDESK_OPERATING_MODE should be absent by default, got %q", v)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// K8s agent RBAC
// ─────────────────────────────────────────────────────────────────────────────

// TestK8sAgentRBACCreated verifies that the ServiceAccount, ClusterRole, and
// ClusterRoleBinding are rendered when rbac.create=true (the default).
func TestK8sAgentRBACCreated(t *testing.T) {
	objects := render(t, "agents.k8s.rbac.create=true")

	resources := []string{
		"ServiceAccount/test-k8s-agent",
		"ClusterRole/test-k8s-agent",
		"ClusterRoleBinding/test-k8s-agent",
	}
	for _, r := range resources {
		if _, ok := objects[r]; !ok {
			t.Errorf("expected %s to be rendered when rbac.create=true", r)
		}
	}
}

// TestK8sAgentRBACSkipped verifies that RBAC resources are NOT rendered when
// rbac.create=false.
func TestK8sAgentRBACSkipped(t *testing.T) {
	objects := render(t, "agents.k8s.rbac.create=false")

	resources := []string{
		"ServiceAccount/test-k8s-agent",
		"ClusterRole/test-k8s-agent",
		"ClusterRoleBinding/test-k8s-agent",
	}
	for _, r := range resources {
		if _, ok := objects[r]; ok {
			t.Errorf("unexpected %s when rbac.create=false", r)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gateway
// ─────────────────────────────────────────────────────────────────────────────

// TestGatewayService verifies the gateway Service is always rendered and uses
// the configured port.
func TestGatewayService(t *testing.T) {
	objects := render(t, "gateway.port=9999")

	svc, ok := objects["Service/test-gateway"]
	if !ok {
		t.Fatal("Service/test-gateway not found")
	}
	spec := svc["spec"].(map[string]any)
	ports, _ := spec["ports"].([]any)
	if len(ports) == 0 {
		t.Fatal("gateway Service has no ports")
	}
	port, _ := ports[0].(map[string]any)
	portNum, _ := port["port"].(int)
	if portNum != 9999 {
		t.Errorf("gateway Service port: want 9999, got %v", portNum)
	}
}

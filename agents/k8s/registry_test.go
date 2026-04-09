package main

import (
	"context"
	"testing"
)

// expectedK8sTools is the canonical list of tools that must be registered in
// NewK8sDirectRegistry. Update this list whenever a tool is added or removed.
var expectedK8sTools = []string{
	"get_pods",
	"get_service",
	"describe_service",
	"get_endpoints",
	"get_events",
	"get_pod_logs",
	"describe_pod",
	"get_nodes",
	"delete_pod",
	"restart_deployment",
	"scale_deployment",
	"get_pod_resources",
	"get_node_status",
}

func TestK8sDirectRegistry_AllToolsRegistered(t *testing.T) {
	r := NewK8sDirectRegistry()
	for _, name := range expectedK8sTools {
		if _, ok := r.Get(name); !ok {
			t.Errorf("tool %q not registered in NewK8sDirectRegistry()", name)
		}
	}
}

// TestK8sArgsToStruct_RoundTrip verifies that map[string]any is correctly
// decoded into a typed struct via the JSON round-trip helper.
func TestK8sArgsToStruct_RoundTrip(t *testing.T) {
	args := map[string]any{
		"namespace": "production",
		"labels":    "app=postgres",
	}
	got, err := k8sArgsToStruct[GetPodsArgs](args)
	if err != nil {
		t.Fatalf("k8sArgsToStruct: %v", err)
	}
	if got.Namespace != "production" {
		t.Errorf("Namespace = %q, want production", got.Namespace)
	}
	if got.Labels != "app=postgres" {
		t.Errorf("Labels = %q, want app=postgres", got.Labels)
	}
}

// TestK8sArgsToStruct_EmptyArgs verifies that an empty map produces a zero-value struct.
func TestK8sArgsToStruct_EmptyArgs(t *testing.T) {
	got, err := k8sArgsToStruct[GetPodsArgs](map[string]any{})
	if err != nil {
		t.Fatalf("k8sArgsToStruct empty: %v", err)
	}
	if got.Namespace != "" {
		t.Errorf("Namespace = %q, want empty", got.Namespace)
	}
}

// TestK8sDirectRegistry_ToolCallable verifies that a registered read tool can
// be called via the registry without panicking. Uses withMockKubectl to avoid
// spawning a real kubectl process.
func TestK8sDirectRegistry_ToolCallable(t *testing.T) {
	defer withMockKubectl("NAME   READY   STATUS    RESTARTS\nnginx  1/1     Running   0\n", nil)()
	r := NewK8sDirectRegistry()
	fn, ok := r.Get("describe_service")
	if !ok {
		t.Fatal("describe_service not registered")
	}
	out, err := fn(context.Background(), map[string]any{
		"namespace":    "default",
		"service_name": "nginx",
	})
	if err != nil {
		t.Fatalf("describe_service via registry: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output from describe_service")
	}
}

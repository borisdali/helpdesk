package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
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

// TestK8sDirectRegistry_ToolCallable verifies that describe_service can be
// called via the registry and returns structured output. Uses a fake clientset
// so no real cluster is required.
func TestK8sDirectRegistry_ToolCallable(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.96.0.1",
			Selector:  map[string]string{"app": "nginx"},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(8080)},
			},
		},
	}
	cs := fake.NewSimpleClientset(svc)
	defer injectFakeClientset("", cs)()

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

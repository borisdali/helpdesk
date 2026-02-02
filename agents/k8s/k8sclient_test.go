package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- diagnoseClientError tests ---

func TestDiagnoseClientError_Nil(t *testing.T) {
	if got := diagnoseClientError(nil); got != nil {
		t.Errorf("diagnoseClientError(nil) = %v, want nil", got)
	}
}

func TestDiagnoseClientError_ContextNotExist(t *testing.T) {
	err := fmt.Errorf("context \"bad-ctx\" does not exist")
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "context does not exist") {
		t.Errorf("got %q, want context-related diagnosis", got)
	}
}

func TestDiagnoseClientError_ConnectionRefused(t *testing.T) {
	err := fmt.Errorf("connection refused")
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "Connection refused") {
		t.Errorf("got %q, want connection refused diagnosis", got)
	}
}

func TestDiagnoseClientError_NetOpError(t *testing.T) {
	err := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: fmt.Errorf("network unreachable"),
	}
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "Cannot reach") {
		t.Errorf("got %q, want network diagnosis", got)
	}
}

func TestDiagnoseClientError_UnableToConnect(t *testing.T) {
	err := fmt.Errorf("unable to connect to the server: something")
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "Cannot reach") {
		t.Errorf("got %q, want cannot reach diagnosis", got)
	}
}

func TestDiagnoseClientError_Timeout(t *testing.T) {
	err := fmt.Errorf("deadline exceeded while waiting for response")
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "timed out") {
		t.Errorf("got %q, want timeout diagnosis", got)
	}
}

func TestDiagnoseClientError_Certificate(t *testing.T) {
	err := fmt.Errorf("x509: certificate has expired")
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "TLS certificate error") {
		t.Errorf("got %q, want cert diagnosis", got)
	}
}

func TestDiagnoseClientError_NoConfig(t *testing.T) {
	err := fmt.Errorf("no configuration has been provided")
	got := diagnoseClientError(err)
	if !strings.Contains(got.Error(), "kubeconfig file is invalid or missing") {
		t.Errorf("got %q, want kubeconfig diagnosis", got)
	}
}

func TestDiagnoseClientError_UnknownPassthrough(t *testing.T) {
	orig := fmt.Errorf("some random error")
	got := diagnoseClientError(orig)
	if got != orig {
		t.Errorf("expected original error to pass through, got %v", got)
	}
}

// --- formatAge tests ---

func TestFormatAge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		created time.Time
		want    string
	}{
		{now.Add(-30 * time.Second), "30s"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-48 * time.Hour), "2d"},
	}
	for _, tt := range tests {
		got := formatAge(tt.created)
		if got != tt.want {
			t.Errorf("formatAge(%v ago) = %q, want %q", now.Sub(tt.created), got, tt.want)
		}
	}
}

// --- podReadyString tests ---

func TestPodReadyString(t *testing.T) {
	tests := []struct {
		name     string
		statuses []corev1.ContainerStatus
		want     string
	}{
		{"all ready", []corev1.ContainerStatus{
			{Ready: true}, {Ready: true},
		}, "2/2"},
		{"some ready", []corev1.ContainerStatus{
			{Ready: true}, {Ready: false}, {Ready: true},
		}, "2/3"},
		{"none ready", []corev1.ContainerStatus{
			{Ready: false}, {Ready: false},
		}, "0/2"},
		{"empty", nil, "0/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podReadyString(tt.statuses)
			if got != tt.want {
				t.Errorf("podReadyString = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- totalRestarts tests ---

func TestTotalRestarts(t *testing.T) {
	tests := []struct {
		name     string
		statuses []corev1.ContainerStatus
		want     int32
	}{
		{"zero", []corev1.ContainerStatus{
			{RestartCount: 0}, {RestartCount: 0},
		}, 0},
		{"mixed", []corev1.ContainerStatus{
			{RestartCount: 3}, {RestartCount: 7},
		}, 10},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := totalRestarts(tt.statuses)
			if got != tt.want {
				t.Errorf("totalRestarts = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- nodeRoles tests ---

func TestNodeRoles(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   []string
	}{
		{"control-plane", map[string]string{
			"node-role.kubernetes.io/control-plane": "",
		}, []string{"control-plane"}},
		{"worker", map[string]string{
			"node-role.kubernetes.io/worker": "",
		}, []string{"worker"}},
		{"multiple roles sorted", map[string]string{
			"node-role.kubernetes.io/worker":        "",
			"node-role.kubernetes.io/control-plane": "",
		}, []string{"control-plane", "worker"}},
		{"no roles", map[string]string{
			"some-other-label": "value",
		}, []string{"<none>"}},
		{"nil labels", nil, []string{"<none>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeRoles(tt.labels)
			if len(got) != len(tt.want) {
				t.Fatalf("nodeRoles = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("nodeRoles[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- nodeStatus tests ---

func TestNodeStatus(t *testing.T) {
	tests := []struct {
		name          string
		conditions    []corev1.NodeCondition
		unschedulable bool
		want          string
	}{
		{"ready", []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}, false, "Ready"},
		{"not ready", []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
		}, false, "NotReady"},
		{"ready + unschedulable", []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}, true, "Ready,SchedulingDisabled"},
		{"unknown (no conditions)", nil, false, "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeStatus(tt.conditions, tt.unschedulable)
			if got != tt.want {
				t.Errorf("nodeStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- externalAddresses tests ---

func TestExternalAddresses(t *testing.T) {
	tests := []struct {
		name string
		svc  corev1.Service
		want []string
	}{
		{
			name: "loadbalancer with IP",
			svc: corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "1.2.3.4"},
						},
					},
				},
			},
			want: []string{"1.2.3.4"},
		},
		{
			name: "loadbalancer with hostname",
			svc: corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{Hostname: "lb.example.com"},
						},
					},
				},
			},
			want: []string{"lb.example.com"},
		},
		{
			name: "external IPs in spec",
			svc: corev1.Service{
				Spec: corev1.ServiceSpec{
					ExternalIPs: []string{"5.6.7.8"},
				},
			},
			want: []string{"5.6.7.8"},
		},
		{
			name: "no external",
			svc:  corev1.Service{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := externalAddresses(tt.svc)
			if len(got) != len(tt.want) {
				t.Fatalf("externalAddresses = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("externalAddresses[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- eventTimestamp tests ---

func TestEventTimestamp(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)
	creation := now.Add(-2 * time.Hour)

	tests := []struct {
		name  string
		event corev1.Event
		want  time.Time
	}{
		{
			name: "prefers LastTimestamp",
			event: corev1.Event{
				LastTimestamp:      metav1.NewTime(now),
				EventTime:         metav1.NewMicroTime(earlier),
				ObjectMeta:        metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(creation)},
			},
			want: now,
		},
		{
			name: "falls back to EventTime",
			event: corev1.Event{
				EventTime:  metav1.NewMicroTime(earlier),
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(creation)},
			},
			want: earlier,
		},
		{
			name: "falls back to CreationTimestamp",
			event: corev1.Event{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(creation)},
			},
			want: creation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventTimestamp(tt.event)
			if !got.Equal(tt.want) {
				t.Errorf("eventTimestamp = %v, want %v", got, tt.want)
			}
		})
	}
}

// Verify that diagnoseClientError wraps net.OpError with connection refused.
func TestDiagnoseClientError_NetOpErrorConnectionRefused(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: inner,
	}
	got := diagnoseClientError(err)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(got.Error(), "Connection refused") {
		t.Errorf("got %q, want connection refused diagnosis", got)
	}
}

// Suppress unused import warning for errors package.
var _ = errors.New

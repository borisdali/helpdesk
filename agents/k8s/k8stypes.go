package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// --- Pod types ---

// PodInfo contains structured information about a Kubernetes pod.
type PodInfo struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Phase      string            `json:"phase"`
	Ready      string            `json:"ready"`
	Restarts   int32             `json:"restarts"`
	Age        string            `json:"age"`
	IP         string            `json:"ip"`
	Node       string            `json:"node"`
	Labels     map[string]string `json:"labels,omitempty"`
	Conditions []string          `json:"conditions,omitempty"`
}

// GetPodsResult is the structured result for the get_pods tool.
type GetPodsResult struct {
	Pods    []PodInfo `json:"pods"`
	Count   int       `json:"count"`
	Message string    `json:"message,omitempty"`
}

// --- Service types ---

// ServicePortInfo describes a single port on a Kubernetes service.
type ServicePortInfo struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"`
	Port       int32  `json:"port"`
	TargetPort string `json:"target_port"`
	NodePort   int32  `json:"node_port,omitempty"`
}

// ServiceInfo contains structured information about a Kubernetes service.
type ServiceInfo struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Type        string            `json:"type"`
	ClusterIP   string            `json:"cluster_ip"`
	ExternalIPs []string          `json:"external_ips,omitempty"`
	Ports       []ServicePortInfo `json:"ports"`
	Selector    map[string]string `json:"selector,omitempty"`
	Age         string            `json:"age"`
}

// GetServiceResult is the structured result for the get_service tool.
type GetServiceResult struct {
	Services []ServiceInfo `json:"services"`
	Count    int           `json:"count"`
	Message  string        `json:"message,omitempty"`
}

// --- Endpoint types ---

// EndpointAddress describes a single endpoint backend address.
type EndpointAddress struct {
	IP       string `json:"ip"`
	NodeName string `json:"node_name,omitempty"`
	PodName  string `json:"pod_name,omitempty"`
	Ready    bool   `json:"ready"`
}

// EndpointPortInfo describes a port exposed by an endpoint.
type EndpointPortInfo struct {
	Name     string `json:"name,omitempty"`
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
}

// EndpointInfo contains structured information about a Kubernetes endpoint.
type EndpointInfo struct {
	Name      string             `json:"name"`
	Namespace string             `json:"namespace"`
	Addresses []EndpointAddress  `json:"addresses"`
	Ports     []EndpointPortInfo `json:"ports"`
}

// GetEndpointsResult is the structured result for the get_endpoints tool.
type GetEndpointsResult struct {
	Endpoints []EndpointInfo `json:"endpoints"`
	Count     int            `json:"count"`
	Message   string         `json:"message,omitempty"`
}

// --- Event types ---

// EventInfo contains structured information about a Kubernetes event.
type EventInfo struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Object    string `json:"object"`
	Source    string `json:"source"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Count     int32  `json:"count"`
}

// GetEventsResult is the structured result for the get_events tool.
type GetEventsResult struct {
	Events  []EventInfo `json:"events"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
}

// --- Node types ---

// NodeInfo contains structured information about a Kubernetes node.
type NodeInfo struct {
	Name             string            `json:"name"`
	Status           string            `json:"status"`
	Roles            []string          `json:"roles"`
	Age              string            `json:"age"`
	KubeletVersion   string            `json:"kubelet_version"`
	InternalIP       string            `json:"internal_ip"`
	ExternalIP       string            `json:"external_ip,omitempty"`
	OSImage          string            `json:"os_image"`
	ContainerRuntime string            `json:"container_runtime"`
	Labels           map[string]string `json:"labels,omitempty"`
}

// GetNodesResult is the structured result for the get_nodes tool.
type GetNodesResult struct {
	Nodes []NodeInfo `json:"nodes"`
	Count int        `json:"count"`
}

// --- Conversion helpers ---

// formatAge converts a creation timestamp to a human-readable age string.
func formatAge(created time.Time) string {
	d := time.Since(created)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// podReadyString returns "N/M" where N is ready containers and M is total.
func podReadyString(statuses []corev1.ContainerStatus) string {
	ready := 0
	for _, s := range statuses {
		if s.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, len(statuses))
}

// totalRestarts sums restart counts across all container statuses.
func totalRestarts(statuses []corev1.ContainerStatus) int32 {
	var total int32
	for _, s := range statuses {
		total += s.RestartCount
	}
	return total
}

// formatPodConditions converts pod conditions to "Type=Status" strings.
func formatPodConditions(conditions []corev1.PodCondition) []string {
	result := make([]string, 0, len(conditions))
	for _, c := range conditions {
		result = append(result, fmt.Sprintf("%s=%s", c.Type, c.Status))
	}
	return result
}

// nodeRoles extracts roles from the standard node-role.kubernetes.io/* labels.
func nodeRoles(labels map[string]string) []string {
	const prefix = "node-role.kubernetes.io/"
	var roles []string
	for k := range labels {
		if strings.HasPrefix(k, prefix) {
			roles = append(roles, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		roles = append(roles, "<none>")
	}
	return roles
}

// nodeStatus returns the human-readable status string for a node.
func nodeStatus(conditions []corev1.NodeCondition, unschedulable bool) string {
	status := "Unknown"
	for _, c := range conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				status = "Ready"
			} else {
				status = "NotReady"
			}
			break
		}
	}
	if unschedulable {
		status += ",SchedulingDisabled"
	}
	return status
}

// externalAddresses extracts external IPs from a service's spec and status.
func externalAddresses(svc corev1.Service) []string {
	var addrs []string
	addrs = append(addrs, svc.Spec.ExternalIPs...)
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			addrs = append(addrs, ing.IP)
		} else if ing.Hostname != "" {
			addrs = append(addrs, ing.Hostname)
		}
	}
	return addrs
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// k8sClient provides cached Kubernetes clientsets keyed by kubeconfig context.
type k8sClient struct {
	mu      sync.Mutex
	clients map[string]*kubernetes.Clientset
}

var sharedClient = &k8sClient{
	clients: make(map[string]*kubernetes.Clientset),
}

// clientset returns a cached *kubernetes.Clientset for the given context.
// If kubeContext is "", it tries in-cluster config first (for running in K8s),
// then falls back to the default kubeconfig context.
func (kc *k8sClient) clientset(kubeContext string) (*kubernetes.Clientset, error) {
	kc.mu.Lock()
	defer kc.mu.Unlock()

	if cs, ok := kc.clients[kubeContext]; ok {
		return cs, nil
	}

	var config *rest.Config
	var err error

	if kubeContext == "" {
		// Try in-cluster config first (when running inside K8s)
		config, err = rest.InClusterConfig()
		if err == nil {
			slog.Info("using in-cluster config")
		} else {
			slog.Debug("in-cluster config not available, falling back to kubeconfig", "err", err)
			// Fall back to default kubeconfig
			config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				clientcmd.NewDefaultClientConfigLoadingRules(),
				&clientcmd.ConfigOverrides{},
			).ClientConfig()
			if err != nil {
				return nil, diagnoseClientError(err)
			}
		}
	} else {
		// Use specified context from kubeconfig
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules, overrides,
		).ClientConfig()
		if err != nil {
			return nil, diagnoseClientError(err)
		}
	}

	config.Timeout = 10 * time.Second

	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, diagnoseClientError(err)
	}

	kc.clients[kubeContext] = cs
	slog.Info("k8s clientset created", "context", kubeContext)
	return cs, nil
}

// diagnoseClientError translates client-go errors into actionable messages.
func diagnoseClientError(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	// Kubeconfig / context errors.
	if strings.Contains(lower, "context") && strings.Contains(lower, "does not exist") {
		return fmt.Errorf("The specified Kubernetes context does not exist in the local kubeconfig. "+
			"Run 'kubectl config get-contexts' to list available contexts, "+
			"or check that the correct kubeconfig file is being used.\n\nRaw error: %v", err)
	}

	// Network errors.
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if strings.Contains(lower, "connection refused") {
			return fmt.Errorf("Connection refused by the Kubernetes API server. "+
				"The cluster may be down, the API server address may be wrong, "+
				"or a VPN/tunnel may need to be active.\n\nRaw error: %v", err)
		}
		return fmt.Errorf("Cannot reach the Kubernetes API server. "+
			"Check network connectivity, verify the cluster is running, "+
			"and confirm the server address in kubeconfig is correct.\n\nRaw error: %v", err)
	}

	// Connection refused without *net.OpError wrapper.
	if strings.Contains(lower, "connection refused") {
		return fmt.Errorf("Connection refused by the Kubernetes API server. "+
			"The cluster may be down, the API server address may be wrong, "+
			"or a VPN/tunnel may need to be active.\n\nRaw error: %v", err)
	}

	// Unable to connect.
	if strings.Contains(lower, "unable to connect to the server") {
		return fmt.Errorf("Cannot reach the Kubernetes API server. "+
			"Check network connectivity, verify the cluster is running, "+
			"and confirm the server address in kubeconfig is correct.\n\nRaw error: %v", err)
	}

	// Kubernetes API status errors.
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		switch {
		case apierrors.IsUnauthorized(statusErr):
			return fmt.Errorf("Authentication to the cluster failed. "+
				"Credentials may have expired. Try re-authenticating "+
				"(e.g., 'gcloud container clusters get-credentials' for GKE).\n\nRaw error: %v", err)
		case apierrors.IsForbidden(statusErr):
			return fmt.Errorf("Permission denied. The current user/service account does not have "+
				"the required RBAC permissions for this operation.\n\nRaw error: %v", err)
		case apierrors.IsNotFound(statusErr):
			if strings.Contains(lower, "namespace") {
				return fmt.Errorf("The specified namespace does not exist in this cluster. "+
					"Run 'kubectl get namespaces' to list available namespaces.\n\nRaw error: %v", err)
			}
			return fmt.Errorf("The requested resource was not found in the cluster. "+
				"Verify the resource name, namespace, and that it has been created.\n\nRaw error: %v", err)
		}
	}

	// Timeout / deadline.
	if os.IsTimeout(err) || strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "i/o timeout") {
		return fmt.Errorf("Request to the Kubernetes API server timed out. "+
			"The cluster may be under heavy load, or there may be network issues.\n\nRaw error: %v", err)
	}

	// TLS / certificate.
	if strings.Contains(lower, "certificate") && (strings.Contains(lower, "expired") ||
		strings.Contains(lower, "invalid") || strings.Contains(lower, "unknown authority")) {
		return fmt.Errorf("TLS certificate error communicating with the cluster. "+
			"The cluster certificate may have expired or the CA is not trusted. "+
			"Re-fetch cluster credentials or update the kubeconfig.\n\nRaw error: %v", err)
	}

	// Missing kubeconfig.
	if strings.Contains(lower, "no configuration") || strings.Contains(lower, "invalid configuration") {
		return fmt.Errorf("The kubeconfig file is invalid or missing. "+
			"Check that ~/.kube/config exists and is correctly formatted, "+
			"or set KUBECONFIG to point to the right file.\n\nRaw error: %v", err)
	}

	return err
}

// --- Fetch functions ---

// fetchPods lists pods using client-go and returns structured results.
func fetchPods(ctx context.Context, kubeContext, namespace, labels string) (GetPodsResult, error) {
	cs, err := sharedClient.clientset(kubeContext)
	if err != nil {
		return GetPodsResult{}, err
	}

	opts := metav1.ListOptions{}
	if labels != "" {
		opts.LabelSelector = labels
	}

	ns := namespace
	if ns == "all" {
		ns = "" // empty namespace = all namespaces in client-go
	}

	podList, err := cs.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return GetPodsResult{}, diagnoseClientError(err)
	}

	if len(podList.Items) == 0 {
		return GetPodsResult{Pods: []PodInfo{}, Message: "No pods found matching the criteria."}, nil
	}

	pods := make([]PodInfo, 0, len(podList.Items))
	for _, p := range podList.Items {
		pods = append(pods, PodInfo{
			Name:       p.Name,
			Namespace:  p.Namespace,
			Phase:      string(p.Status.Phase),
			Ready:      podReadyString(p.Status.ContainerStatuses),
			Restarts:   totalRestarts(p.Status.ContainerStatuses),
			Age:        formatAge(p.CreationTimestamp.Time),
			IP:         p.Status.PodIP,
			Node:       p.Spec.NodeName,
			Labels:     p.Labels,
			Conditions: formatPodConditions(p.Status.Conditions),
		})
	}

	return GetPodsResult{Pods: pods, Count: len(pods)}, nil
}

// fetchServices lists services using client-go and returns structured results.
func fetchServices(ctx context.Context, kubeContext, namespace, serviceName, serviceType string) (GetServiceResult, error) {
	cs, err := sharedClient.clientset(kubeContext)
	if err != nil {
		return GetServiceResult{}, err
	}

	opts := metav1.ListOptions{}
	if serviceName != "" {
		opts.FieldSelector = fmt.Sprintf("metadata.name=%s", serviceName)
	}

	svcList, err := cs.CoreV1().Services(namespace).List(ctx, opts)
	if err != nil {
		return GetServiceResult{}, diagnoseClientError(err)
	}

	var services []ServiceInfo
	for _, svc := range svcList.Items {
		if serviceType != "" && string(svc.Spec.Type) != serviceType {
			continue
		}

		ports := make([]ServicePortInfo, 0, len(svc.Spec.Ports))
		for _, p := range svc.Spec.Ports {
			ports = append(ports, ServicePortInfo{
				Name:       p.Name,
				Protocol:   string(p.Protocol),
				Port:       p.Port,
				TargetPort: p.TargetPort.String(),
				NodePort:   p.NodePort,
			})
		}

		services = append(services, ServiceInfo{
			Name:        svc.Name,
			Namespace:   svc.Namespace,
			Type:        string(svc.Spec.Type),
			ClusterIP:   svc.Spec.ClusterIP,
			ExternalIPs: externalAddresses(svc),
			Ports:       ports,
			Selector:    svc.Spec.Selector,
			Age:         formatAge(svc.CreationTimestamp.Time),
		})
	}

	if len(services) == 0 {
		return GetServiceResult{Services: []ServiceInfo{}, Message: "No services found matching the criteria."}, nil
	}
	return GetServiceResult{Services: services, Count: len(services)}, nil
}

// fetchEndpoints lists endpoints using client-go and returns structured results.
func fetchEndpoints(ctx context.Context, kubeContext, namespace, endpointName string) (GetEndpointsResult, error) {
	cs, err := sharedClient.clientset(kubeContext)
	if err != nil {
		return GetEndpointsResult{}, err
	}

	opts := metav1.ListOptions{}
	if endpointName != "" {
		opts.FieldSelector = fmt.Sprintf("metadata.name=%s", endpointName)
	}

	epList, err := cs.CoreV1().Endpoints(namespace).List(ctx, opts)
	if err != nil {
		return GetEndpointsResult{}, diagnoseClientError(err)
	}

	if len(epList.Items) == 0 {
		return GetEndpointsResult{Endpoints: []EndpointInfo{}, Message: "No endpoints found. This may indicate no pods match the service selector."}, nil
	}

	endpoints := make([]EndpointInfo, 0, len(epList.Items))
	for _, ep := range epList.Items {
		var addresses []EndpointAddress
		var ports []EndpointPortInfo

		for _, subset := range ep.Subsets {
			for _, addr := range subset.Addresses {
				ea := EndpointAddress{
					IP:    addr.IP,
					Ready: true,
				}
				if addr.NodeName != nil {
					ea.NodeName = *addr.NodeName
				}
				if addr.TargetRef != nil && addr.TargetRef.Kind == "Pod" {
					ea.PodName = addr.TargetRef.Name
				}
				addresses = append(addresses, ea)
			}
			for _, addr := range subset.NotReadyAddresses {
				ea := EndpointAddress{
					IP:    addr.IP,
					Ready: false,
				}
				if addr.NodeName != nil {
					ea.NodeName = *addr.NodeName
				}
				if addr.TargetRef != nil && addr.TargetRef.Kind == "Pod" {
					ea.PodName = addr.TargetRef.Name
				}
				addresses = append(addresses, ea)
			}
			for _, p := range subset.Ports {
				ports = append(ports, EndpointPortInfo{
					Name:     p.Name,
					Port:     p.Port,
					Protocol: string(p.Protocol),
				})
			}
		}

		endpoints = append(endpoints, EndpointInfo{
			Name:      ep.Name,
			Namespace: ep.Namespace,
			Addresses: addresses,
			Ports:     ports,
		})
	}

	return GetEndpointsResult{Endpoints: endpoints, Count: len(endpoints)}, nil
}

// fetchEvents lists events using client-go and returns structured results.
func fetchEvents(ctx context.Context, kubeContext, namespace, resourceName, eventType string) (GetEventsResult, error) {
	cs, err := sharedClient.clientset(kubeContext)
	if err != nil {
		return GetEventsResult{}, err
	}

	opts := metav1.ListOptions{}
	if eventType != "" {
		opts.FieldSelector = fmt.Sprintf("type=%s", eventType)
	}

	eventList, err := cs.CoreV1().Events(namespace).List(ctx, opts)
	if err != nil {
		return GetEventsResult{}, diagnoseClientError(err)
	}

	// Sort by LastTimestamp descending.
	sort.Slice(eventList.Items, func(i, j int) bool {
		ti := eventTimestamp(eventList.Items[i])
		tj := eventTimestamp(eventList.Items[j])
		return tj.Before(ti)
	})

	var events []EventInfo
	for _, e := range eventList.Items {
		if resourceName != "" && !strings.Contains(e.InvolvedObject.Name, resourceName) {
			continue
		}
		events = append(events, EventInfo{
			Type:      e.Type,
			Reason:    e.Reason,
			Message:   e.Message,
			Object:    fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
			Source:    e.Source.Component,
			FirstSeen: formatEventTime(e.FirstTimestamp.Time),
			LastSeen:  formatEventTime(e.LastTimestamp.Time),
			Count:     e.Count,
		})
	}

	if len(events) == 0 {
		return GetEventsResult{Events: []EventInfo{}, Message: "No events found matching the criteria."}, nil
	}
	return GetEventsResult{Events: events, Count: len(events)}, nil
}

// eventTimestamp returns the most meaningful timestamp for sorting.
// Prefers LastTimestamp, falls back to EventTime, then CreationTimestamp.
func eventTimestamp(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if e.EventTime.Time.IsZero() {
		return e.CreationTimestamp.Time
	}
	return e.EventTime.Time
}

// formatEventTime formats a time for event display. Returns empty string for zero times.
func formatEventTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// fetchNodes lists nodes using client-go and returns structured results.
func fetchNodes(ctx context.Context, kubeContext string, showLabels bool) (GetNodesResult, error) {
	cs, err := sharedClient.clientset(kubeContext)
	if err != nil {
		return GetNodesResult{}, err
	}

	nodeList, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return GetNodesResult{}, diagnoseClientError(err)
	}

	nodes := make([]NodeInfo, 0, len(nodeList.Items))
	for _, n := range nodeList.Items {
		info := NodeInfo{
			Name:             n.Name,
			Status:           nodeStatus(n.Status.Conditions, n.Spec.Unschedulable),
			Roles:            nodeRoles(n.Labels),
			Age:              formatAge(n.CreationTimestamp.Time),
			KubeletVersion:   n.Status.NodeInfo.KubeletVersion,
			OSImage:          n.Status.NodeInfo.OSImage,
			ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
		}

		for _, addr := range n.Status.Addresses {
			switch addr.Type {
			case corev1.NodeInternalIP:
				info.InternalIP = addr.Address
			case corev1.NodeExternalIP:
				info.ExternalIP = addr.Address
			}
		}

		if showLabels {
			info.Labels = n.Labels
		}

		nodes = append(nodes, info)
	}

	return GetNodesResult{Nodes: nodes, Count: len(nodes)}, nil
}

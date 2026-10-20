package main

// Kubernetes side of the instance lifecycle (lifecycle.go): a self-managed
// Postgres StatefulSet whose image carries pgBackRest and its config. The
// instance is found from the target's k8s_pod_selector; the StatefulSet is
// the one whose pod template carries those labels.
//
// v0.32 scope: one replica (ordinal 0's volume claims), a pgBackRest repo on
// a volume of the StatefulSet (posix repo), an image that runs as root (the
// runner switches to postgres with su), and a single-node cluster or volumes
// that attach anywhere (the runner pod isn't pinned to a node). Multi-node
// ReadWriteOnce pinning and remote repos (S3/GCS/Azure credentials) are
// v0.33.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// k8sResumeReplicasAnnotation records, on the StatefulSet, the replica count
// quiesce scaled down from, for resume.
const k8sResumeReplicasAnnotation = "helpdesk.io/resume-replicas"

// k8sMaintenanceTimeout bounds one maintenance pod: a restore copies the whole
// backup, so this is generous.
var k8sMaintenanceTimeout = 30 * time.Minute

// k8sQuiesceTimeout bounds waiting for the instance's pod to go away.
var k8sQuiesceTimeout = 3 * time.Minute

// k8sPollInterval is how often pod state is polled.
var k8sPollInterval = 2 * time.Second

// kubectl runs kubectl in host's context and namespace.
func kubectl(ctx context.Context, host resolvedHost, args ...string) (string, error) {
	full := []string{"-n", host.K8sNamespace}
	if host.K8sContext != "" {
		full = append([]string{"--context", host.K8sContext}, full...)
	}
	out, err := cmdRunner.Run(ctx, "kubectl", append(full, args...), nil)
	if err != nil {
		return out, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}

// k8sInstancePod returns the instance's pod and whether it is ready (phase
// Running, every container ready); "" when there is none (scaled to 0).
func k8sInstancePod(ctx context.Context, host resolvedHost) (string, bool, error) {
	out, err := kubectl(ctx, host, "get", "pod", "-l", host.K8sPodSelector, "-o", "json")
	if err != nil {
		return "", false, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			k8sPodStatusJSON
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return "", false, fmt.Errorf("parsing kubectl get pod: %w", err)
	}
	if len(list.Items) == 0 {
		return "", false, nil
	}
	p := list.Items[0]
	ready := p.Status.Phase == "Running" && len(p.Status.ContainerStatuses) > 0
	for _, c := range p.Status.ContainerStatuses {
		ready = ready && c.Ready
	}
	return p.Metadata.Name, ready, nil
}

// k8sStatefulSetJSON is the part of a StatefulSet the lifecycle reads.
type k8sStatefulSetJSON struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int `json:"replicas"`
		Template struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Volumes    []json.RawMessage `json:"volumes"`
				Containers []struct {
					Name         string            `json:"name"`
					Image        string            `json:"image"`
					VolumeMounts []json.RawMessage `json:"volumeMounts"`
				} `json:"containers"`
				ImagePullSecrets []json.RawMessage `json:"imagePullSecrets"`
			} `json:"spec"`
		} `json:"template"`
		VolumeClaimTemplates []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"volumeClaimTemplates"`
	} `json:"spec"`
}

// selectorMatches reports whether labels satisfy an equality-based label
// selector ("app=x,role=y"), the form k8s_pod_selector takes.
func selectorMatches(selector string, labels map[string]string) bool {
	if strings.TrimSpace(selector) == "" {
		return false
	}
	for _, term := range strings.Split(selector, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(term), "=")
		if !ok || labels[strings.TrimSpace(k)] != strings.TrimSpace(strings.TrimPrefix(v, "=")) {
			return false
		}
	}
	return true
}

// k8sStatefulSet returns the StatefulSet whose pod template matches the
// target's pod selector; exactly one must.
func k8sStatefulSet(ctx context.Context, host resolvedHost) (k8sStatefulSetJSON, error) {
	out, err := kubectl(ctx, host, "get", "statefulset", "-o", "json")
	if err != nil {
		return k8sStatefulSetJSON{}, err
	}
	var list struct {
		Items []k8sStatefulSetJSON `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return k8sStatefulSetJSON{}, fmt.Errorf("parsing kubectl get statefulset: %w", err)
	}
	var found []k8sStatefulSetJSON
	for _, s := range list.Items {
		if selectorMatches(host.K8sPodSelector, s.Spec.Template.Metadata.Labels) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return k8sStatefulSetJSON{}, fmt.Errorf("no StatefulSet in namespace %q has pods matching %q: restore on Kubernetes needs a StatefulSet (self-managed Postgres)", host.K8sNamespace, host.K8sPodSelector)
	default:
		return k8sStatefulSetJSON{}, fmt.Errorf("%d StatefulSets in namespace %q have pods matching %q — refusing to guess which one", len(found), host.K8sNamespace, host.K8sPodSelector)
	}
}

// k8sQuiesce scales the instance's StatefulSet to 0 and waits for its pod to
// go, recording the replica count it had for k8sResume. Already at 0 (a
// quiesce that ran before) is fine; the recorded count is kept.
func k8sQuiesce(ctx context.Context, host resolvedHost) (string, error) {
	sts, err := k8sStatefulSet(ctx, host)
	if err != nil {
		return "", err
	}
	replicas := 1
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	name := sts.Metadata.Name
	if replicas > 0 {
		if _, err := kubectl(ctx, host, "annotate", "statefulset", name, "--overwrite",
			fmt.Sprintf("%s=%d", k8sResumeReplicasAnnotation, replicas)); err != nil {
			return "", err
		}
		if _, err := kubectl(ctx, host, "scale", "statefulset", name, "--replicas=0"); err != nil {
			return "", err
		}
	}
	deadline := time.Now().Add(k8sQuiesceTimeout)
	for {
		pod, _, err := k8sInstancePod(ctx, host)
		if err == nil && pod == "" {
			return fmt.Sprintf("StatefulSet %s scaled from %d to 0 replicas; resume restores %d", name, replicas, k8sResumeReplicas(sts, replicas)), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("StatefulSet %s scaled to 0 but its pod %q is still there after %s", name, pod, k8sQuiesceTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
}

// k8sResumeReplicas is the replica count resume scales to: the recorded one,
// else the current one if above 0, else 1.
func k8sResumeReplicas(sts k8sStatefulSetJSON, current int) int {
	if n, err := strconv.Atoi(sts.Metadata.Annotations[k8sResumeReplicasAnnotation]); err == nil && n > 0 {
		return n
	}
	if current > 0 {
		return current
	}
	return 1
}

// k8sResume scales the StatefulSet back to the replica count quiesce
// recorded and clears the record. It doesn't wait for postgres: recovery
// runs on startup, and the playbook checks the instance comes up.
func k8sResume(ctx context.Context, host resolvedHost) (string, error) {
	sts, err := k8sStatefulSet(ctx, host)
	if err != nil {
		return "", err
	}
	current := 0
	if sts.Spec.Replicas != nil {
		current = *sts.Spec.Replicas
	}
	n := k8sResumeReplicas(sts, current)
	name := sts.Metadata.Name
	if _, err := kubectl(ctx, host, "scale", "statefulset", name, fmt.Sprintf("--replicas=%d", n)); err != nil {
		return "", err
	}
	if _, ok := sts.Metadata.Annotations[k8sResumeReplicasAnnotation]; ok {
		if _, err := kubectl(ctx, host, "annotate", "statefulset", name, k8sResumeReplicasAnnotation+"-"); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("StatefulSet %s scaled from %d to %d replicas", name, current, n), nil
}

// k8sMaintenancePodOverrides is the pod spec of a maintenance pod: the
// StatefulSet's image and its postgres container's volume mounts, with the
// StatefulSet's own volumes and ordinal 0's volume claims, running script as
// the postgres OS user. The container is named after the pod: kubectl run
// applies --overrides as a strategic merge, which merges containers by name,
// so any other name would add a second container beside the generated one.
func k8sMaintenancePodOverrides(sts k8sStatefulSetJSON, podName, script string) (string, string, error) {
	if len(sts.Spec.Template.Spec.Containers) == 0 {
		return "", "", fmt.Errorf("StatefulSet %s has no containers", sts.Metadata.Name)
	}
	c := sts.Spec.Template.Spec.Containers[0]
	volumes := append([]json.RawMessage{}, sts.Spec.Template.Spec.Volumes...)
	for _, vct := range sts.Spec.VolumeClaimTemplates {
		v, _ := json.Marshal(map[string]any{
			"name":                  vct.Metadata.Name,
			"persistentVolumeClaim": map[string]any{"claimName": vct.Metadata.Name + "-" + sts.Metadata.Name + "-0"},
		})
		volumes = append(volumes, v)
	}
	spec := map[string]any{
		"restartPolicy": "Never",
		"volumes":       volumes,
		"containers": []map[string]any{{
			"name":            podName,
			"image":           c.Image,
			"imagePullPolicy": "IfNotPresent",
			"command":         []string{"su", "postgres", "-c", script},
			"volumeMounts":    c.VolumeMounts,
		}},
	}
	if len(sts.Spec.Template.Spec.ImagePullSecrets) > 0 {
		spec["imagePullSecrets"] = sts.Spec.Template.Spec.ImagePullSecrets
	}
	b, err := json.Marshal(map[string]any{"apiVersion": "v1", "spec": spec})
	return string(b), c.Image, err
}

// k8sMaintenance runs script in a one-off pod (see k8sMaintenancePodOverrides)
// and returns its output. The pod is deleted afterwards either way. Used when
// the instance's own pod can't be exec'd into, and always for the restore
// itself, which runs after quiesce.
func k8sMaintenance(ctx context.Context, host resolvedHost, script string) (string, error) {
	sts, err := k8sStatefulSet(ctx, host)
	if err != nil {
		return "", err
	}
	name := sts.Metadata.Name + "-helpdesk-maint-" + uuid.New().String()[:8]
	overrides, image, err := k8sMaintenancePodOverrides(sts, name, script)
	if err != nil {
		return "", err
	}
	if _, err := kubectl(ctx, host, "run", name, "--image="+image, "--restart=Never",
		"--labels=app.kubernetes.io/managed-by=helpdesk,helpdesk.io/maintenance-for="+sts.Metadata.Name,
		"--overrides="+overrides); err != nil {
		return "", err
	}
	defer func() {
		_, _ = kubectl(context.WithoutCancel(ctx), host, "delete", "pod", name, "--wait=false")
	}()

	deadline := time.Now().Add(k8sMaintenanceTimeout)
	phase := ""
	for {
		out, err := kubectl(ctx, host, "get", "pod", name, "-o", "jsonpath={.status.phase}")
		if err == nil {
			phase = strings.TrimSpace(out)
			if phase == "Succeeded" || phase == "Failed" {
				break
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("maintenance pod %s still %q after %s", name, phase, k8sMaintenanceTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
	logs, err := kubectl(ctx, host, "logs", name)
	if err != nil {
		return "", err
	}
	if phase == "Failed" {
		code, _ := kubectl(ctx, host, "get", "pod", name, "-o", "jsonpath={.status.containerStatuses[0].state.terminated.exitCode}")
		return logs, fmt.Errorf("maintenance pod %s failed (exit code %s)", name, strings.TrimSpace(code))
	}
	return logs, nil
}

// k8sLogs returns the instance pod's recent output: for a container that has
// restarted (crash-looping), the previous run's output first, which holds
// the crash, then the current one's.
func k8sLogs(ctx context.Context, host resolvedHost, lines int) (string, error) {
	pod, _, err := k8sInstancePod(ctx, host)
	if err != nil {
		return "", err
	}
	if pod == "" {
		return "", fmt.Errorf("no pod matches %q in namespace %q (StatefulSet scaled to 0?)", host.K8sPodSelector, host.K8sNamespace)
	}
	tail := fmt.Sprintf("--tail=%d", lines)
	var b strings.Builder
	if prev, err := kubectl(ctx, host, "logs", pod, "--previous", tail); err == nil && strings.TrimSpace(prev) != "" {
		b.WriteString("--- previous container run ---\n")
		b.WriteString(prev)
		if !strings.HasSuffix(prev, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- current container run ---\n")
	}
	cur, err := kubectl(ctx, host, "logs", pod, tail)
	if err != nil {
		if b.Len() > 0 {
			return b.String(), nil // crash-looping: the current run may not have started yet
		}
		return "", err
	}
	b.WriteString(cur)
	return b.String(), nil
}

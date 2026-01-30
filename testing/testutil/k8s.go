package testutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// KustomizeApply applies a Kustomize overlay directory.
func KustomizeApply(ctx context.Context, overlayDir string, kubeContext string) error {
	args := []string{"apply", "-k", overlayDir}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply -k %s: %v\n%s", overlayDir, err, output)
	}
	return nil
}

// KustomizeDelete deletes resources defined by a Kustomize overlay.
func KustomizeDelete(ctx context.Context, overlayDir string, kubeContext string) error {
	args := []string{"delete", "-k", overlayDir, "--ignore-not-found"}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl delete -k %s: %v\n%s", overlayDir, err, output)
	}
	return nil
}

// KubectlApply applies a YAML directory or file.
func KubectlApply(ctx context.Context, path string, kubeContext string) error {
	args := []string{"apply", "-k", path}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply -k %s: %v\n%s", path, err, output)
	}
	return nil
}

// KubectlRun runs an arbitrary kubectl command.
func KubectlRun(ctx context.Context, kubeContext string, args ...string) (string, error) {
	if kubeContext != "" {
		args = append([]string{"--context", kubeContext}, args...)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output), nil
}

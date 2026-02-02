package main

import "testing"

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

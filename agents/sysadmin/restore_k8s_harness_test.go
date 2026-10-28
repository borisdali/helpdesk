//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/agentutil"
)

// The in-cluster harness refuses to start destroying a pod when the agent will
// not answer it. This is the check that was missing when a run without an API
// key crashed the pod and then could not repair it (2026-10-28).
func TestK8sHarness_AuthProblem(t *testing.T) {
	var gotAuth string
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusUnauthorized {
			json.NewEncoder(w).Encode(agentutil.DirectToolResponse{Error: "authentication required: this endpoint requires authentication"}) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(agentutil.DirectToolResponse{Output: `{"status_code":0}`}) //nolint:errcheck
	}))
	defer srv.Close()

	c := &k8sCluster{t: t, agentURL: srv.URL, target: "pgbackrest-k8s-destruct"}
	msg := c.authProblem()
	if !strings.Contains(msg, "K8S_SYSADMIN_API_KEY") || !strings.Contains(msg, "Nothing was changed") {
		t.Errorf("a rejected call: %q", msg)
	}
	if gotAuth != "" {
		t.Errorf("sent Authorization %q with no key configured", gotAuth)
	}

	// With a key, it is sent as a bearer token, and an accepted call is fine.
	status = http.StatusOK
	c.apiKey = "secret-key"
	if msg := c.authProblem(); msg != "" {
		t.Errorf("an accepted call reported a problem: %q", msg)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

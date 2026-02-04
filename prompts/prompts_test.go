package prompts

import (
	"strings"
	"testing"
)

func TestPrompts_NonEmpty(t *testing.T) {
	prompts := map[string]string{
		"Orchestrator": Orchestrator,
		"Database":     Database,
		"K8s":          K8s,
		"Incident":     Incident,
	}

	for name, content := range prompts {
		t.Run(name, func(t *testing.T) {
			if content == "" {
				t.Errorf("%s prompt is empty", name)
			}
			if len(content) < 100 {
				t.Errorf("%s prompt suspiciously short: %d bytes", name, len(content))
			}
		})
	}
}

func TestPrompts_ExpectedKeywords(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		keywords []string
	}{
		{
			"Database",
			Database,
			[]string{"connection_string", "PostgreSQL"},
		},
		{
			"K8s",
			K8s,
			[]string{"pods", "service", "endpoints", "Kubernetes"},
		},
		{
			"Incident",
			Incident,
			[]string{"create_incident_bundle", "incident", "bundle"},
		},
		{
			"Orchestrator",
			Orchestrator,
			[]string{"delegate", "agent", "database", "kubernetes"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lower := strings.ToLower(tc.content)
			for _, kw := range tc.keywords {
				if !strings.Contains(lower, strings.ToLower(kw)) {
					t.Errorf("%s prompt missing keyword %q", tc.name, kw)
				}
			}
		})
	}
}

func TestPrompts_CriticalInstructions(t *testing.T) {
	// Database and K8s agents should have fail-fast instructions.
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"Database", Database},
		{"K8s", K8s},
	} {
		t.Run(tc.name+"_FailFast", func(t *testing.T) {
			lower := strings.ToLower(tc.content)
			if !strings.Contains(lower, "fail fast") && !strings.Contains(lower, "stop immediately") {
				t.Errorf("%s prompt should contain fail-fast instructions", tc.name)
			}
		})
	}
}

func TestPrompts_OrchestratorRouting(t *testing.T) {
	// Orchestrator should mention the specialist agents it can route to.
	lower := strings.ToLower(Orchestrator)
	agents := []string{"database", "k8s", "incident"}

	for _, agent := range agents {
		if !strings.Contains(lower, agent) {
			t.Errorf("Orchestrator prompt should mention %s agent", agent)
		}
	}
}

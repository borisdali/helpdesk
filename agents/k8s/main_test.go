package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeK8sRulesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadK8sEvidenceRules_ValidFile guards specifically against a wrong map
// key (e.g. "get_pod" typo'd for "get_pods") silently leaving one tool's
// rules empty forever — the exact bug class this test exists to catch would
// not be caught by internal/evidence's own tests, since LoadRules itself
// doesn't know which tool names the K8s agent cares about; only this
// integration point does.
func TestLoadK8sEvidenceRules_ValidFile(t *testing.T) {
	path := writeK8sRulesFile(t, `- tool: get_pods
  probe: restart_count
  operator: ">"
  threshold: 0
  signal: pod_restarted
- tool: get_events
  probe: reason
  operator: "=="
  threshold: "Evicted"
  signal: evicted
`)
	podRules, eventRules := loadK8sEvidenceRules(path)
	if len(podRules) != 1 {
		t.Errorf("got %d get_pods rules, want 1", len(podRules))
	}
	if len(eventRules) != 1 {
		t.Errorf("got %d get_events rules, want 1", len(eventRules))
	}
	if len(podRules) == 1 && podRules[0].Signal != "pod_restarted" {
		t.Errorf("got get_pods signal %q, want pod_restarted", podRules[0].Signal)
	}
	if len(eventRules) == 1 && eventRules[0].Signal != "evicted" {
		t.Errorf("got get_events signal %q, want evicted", eventRules[0].Signal)
	}
}

func TestLoadK8sEvidenceRules_OnlyOneToolPresent(t *testing.T) {
	path := writeK8sRulesFile(t, `- tool: get_pods
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
`)
	podRules, eventRules := loadK8sEvidenceRules(path)
	if len(podRules) != 1 {
		t.Errorf("got %d get_pods rules, want 1", len(podRules))
	}
	if len(eventRules) != 0 {
		t.Errorf("got %d get_events rules, want 0 (file never mentions get_events)", len(eventRules))
	}
}

func TestLoadK8sEvidenceRules_MalformedFile_ReturnsNilNil(t *testing.T) {
	path := writeK8sRulesFile(t, `- tool: get_pods
  probe: restat_count
  operator: ">"
  threshold: 0
  signal: pod_restarted
`) // "restat_count" — typo, unknown probe
	podRules, eventRules := loadK8sEvidenceRules(path)
	if podRules != nil || eventRules != nil {
		t.Errorf("got (%v, %v), want (nil, nil) for a malformed rules file", podRules, eventRules)
	}
}

func TestLoadK8sEvidenceRules_MissingFile_ReturnsNilNil(t *testing.T) {
	podRules, eventRules := loadK8sEvidenceRules(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if podRules != nil || eventRules != nil {
		t.Errorf("got (%v, %v), want (nil, nil) for a missing file", podRules, eventRules)
	}
}

// TestObjectiveEvidenceYAML_Valid is a narrower, more directly diagnosable
// duplicate of what TestMain (tools_test.go) already enforces by panicking
// on load failure — that panic kills the whole package's test run with a
// clear message, but a dedicated failing test here is easier to spot in CI
// output than "the package's TestMain panicked."
func TestObjectiveEvidenceYAML_Valid(t *testing.T) {
	podRules, eventRules := loadK8sEvidenceRules("objective_evidence.yaml")
	if len(podRules) == 0 {
		t.Error("expected at least one get_pods rule from the real shipped file")
	}
	if len(eventRules) == 0 {
		t.Error("expected at least one get_events rule from the real shipped file")
	}
}

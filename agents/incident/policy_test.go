package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"helpdesk/agentutil"
)

// newDenyIncidentWriteEnforcer mirrors agents/sysadmin/pgbackrest_test.go's
// own newDenyHostWriteEnforcer, for the "incident" resource type this
// package's policyEnforcer.CheckTool call uses.
func newDenyIncidentWriteEnforcer(t *testing.T) *agentutil.PolicyEnforcer {
	t.Helper()
	const yaml = `
version: "1"
policies:
  - name: deny-incident-write
    resources:
      - type: incident
    rules:
      - action: write
        effect: deny
        message: "incident bundle creation is not permitted in this test"
`
	f, err := os.CreateTemp(t.TempDir(), "incident-policies-*.yaml")
	if err != nil {
		t.Fatalf("create temp policy file: %v", err)
	}
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatalf("write temp policy file: %v", err)
	}
	_ = f.Close()

	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    f.Name(),
		DefaultPolicy: "allow",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

// TestCreateIncidentBundleImpl_PolicyDenied is a regression test for a real
// gap found live 2026-09-30: agents/incident had no policy engine
// integration at all — main.go hardcoded its governance log line to
// "policy": false rather than reading cfg.PolicyEnabled, and nothing in this
// package ever called policyEnforcer.CheckTool. HELPDESK_POLICY_ENABLED/
// HELPDESK_POLICY_FILE were silently ignored regardless of what an operator
// set them to, so create_incident_bundle (this agent's one
// ActionWrite-classified tool) had no operating-mode check, no tag-based
// rules, no blast-radius bounds — nothing gating it at all.
func TestCreateIncidentBundleImpl_PolicyDenied(t *testing.T) {
	withMockedLayers(t)
	t.Setenv("HELPDESK_INCIDENT_DIR", t.TempDir())

	old := policyEnforcer
	policyEnforcer = newDenyIncidentWriteEnforcer(t)
	t.Cleanup(func() { policyEnforcer = old })

	_, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{
		InfraKey:    "prod-db",
		Description: "test bundle",
	})
	if err == nil {
		t.Fatal("createIncidentBundleImpl() error = nil, want a policy denial")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("error = %v, want it to mention 'not permitted'", err)
	}
}

// TestCreateIncidentBundleImpl_NoPolicyEnforcer_StillWorks confirms the
// pre-existing, unconfigured-policy-engine behavior (policyEnforcer == nil)
// is unchanged — every existing test in this package relies on this.
func TestCreateIncidentBundleImpl_NoPolicyEnforcer_StillWorks(t *testing.T) {
	withMockedLayers(t)
	t.Setenv("HELPDESK_INCIDENT_DIR", t.TempDir())

	old := policyEnforcer
	policyEnforcer = nil
	t.Cleanup(func() { policyEnforcer = old })

	if _, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{
		InfraKey: "prod-db",
	}); err != nil {
		t.Fatalf("createIncidentBundleImpl() error = %v, want nil when policyEnforcer is unset", err)
	}
}

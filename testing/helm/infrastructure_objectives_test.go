package helm_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

// A chart value that carries recovery objectives and environments must come out
// as an infrastructure.json the Go loader accepts and resolves as written, and
// reach the sysadmin agent, which is the one that reads them. The chart passes
// `infrastructure` through as arbitrary JSON and has no values schema, so this
// is the test that would notice a template that started to pick keys.

const objectivesInfrastructure = `{
  "defaults": {"objectives": {"rpo": "1h", "rto": "4h", "availability_slo": 99.0}},
  "environments": {
    "prod":    {"description": "customer-facing", "objectives": {"rpo": "5m", "rto": "30m", "availability_slo": 99.9, "availability_window": "30d", "recovery_preference": "data_first", "enforcement": "acknowledge", "availability_enforcement": "acknowledge"}},
    "staging": {"objectives": {"rpo": "1d"}}
  },
  "db_servers": {
    "orders": {"name": "orders", "connection_string": "host=orders port=5432 dbname=o", "environment": "prod"},
    "ledger": {"name": "ledger", "connection_string": "host=ledger port=5432 dbname=l", "environment": "prod",
               "objectives": {"rpo": "0s", "enforcement": "refuse"}},
    "scratch": {"name": "scratch", "connection_string": "host=scratch port=5432 dbname=s"}
  }
}`

// renderedInfrastructure returns the infrastructure.json the chart writes to its
// ConfigMap, or "" when it writes none.
func renderedInfrastructure(t *testing.T, objs map[string]map[string]any) string {
	t.Helper()
	for key, obj := range objs {
		if !strings.HasPrefix(key, "ConfigMap/") {
			continue
		}
		if data, _ := obj["data"].(map[string]any); data != nil {
			if s, ok := data["infrastructure.json"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func loadRendered(t *testing.T, body string) *infra.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "infrastructure.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(p)
	if err != nil {
		t.Fatalf("the Go loader rejects what the chart rendered: %v\n%s", err, body)
	}
	return cfg
}

func TestInfrastructureObjectivesSurviveTheChart(t *testing.T) {
	objs := render(t, "setjson:infrastructure="+objectivesInfrastructure)

	body := renderedInfrastructure(t, objs)
	if body == "" {
		t.Fatal("no infrastructure.json in the rendered ConfigMap")
	}
	cfg := loadRendered(t, body)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the rendered objectives are invalid: %v", err)
	}

	// Resolved the way the sysadmin agent will resolve them, level by level.
	orders, ok := cfg.ResolveObjectives("orders")
	if !ok || orders.RPO.Value != 5*time.Minute || orders.RPO.Source != "environment:prod" ||
		orders.RTO.Value != 30*time.Minute || orders.RecoveryPreference.Value != "data_first" ||
		orders.EffectiveEnforcement() != infra.EnforceAcknowledge {
		t.Errorf("orders = %+v", orders)
	}
	// The availability window and the budget it gives survive the chart too: a
	// 30m rto in 43m12s of downtime, tolerating one recovery, and no contradiction.
	if !orders.AvailabilityWindow.Set || orders.AvailabilityWindow.Value != 30*24*time.Hour || orders.AvailabilityWindow.Source != "environment:prod" {
		t.Errorf("orders window = %+v", orders.AvailabilityWindow)
	}
	if b := orders.Budget(); !b.Computed || b.Allowed != 43*time.Minute+12*time.Second || b.Tolerated != 1 || b.Contradictory {
		t.Errorf("orders budget = %+v", b)
	}
	if got := cfg.Consistency(); len(got) != 0 {
		t.Errorf("the rendered objectives contradict each other: %v", got)
	}
	// The budget's own enforcement survives the chart, and is not the rpo's: the
	// ledger database sets enforcement: refuse for itself and says nothing about
	// the budget, so the budget stays at the environment's acknowledge.
	if orders.EffectiveAvailabilityEnforcement() != infra.EnforceAcknowledge {
		t.Errorf("orders availability enforcement = %q", orders.EffectiveAvailabilityEnforcement())
	}
	ledger, _ := cfg.ResolveObjectives("ledger")
	if ledger.EffectiveEnforcement() != infra.EnforceRefuse || ledger.EffectiveAvailabilityEnforcement() != infra.EnforceAcknowledge {
		t.Errorf("ledger: rpo enforcement %q, availability enforcement %q", ledger.EffectiveEnforcement(), ledger.EffectiveAvailabilityEnforcement())
	}
	// A zero rpo is a value (no loss tolerated), and a database's own enforcement wins.
	if !ledger.RPO.Set || ledger.RPO.Value != 0 || ledger.RPO.Source != "database" || ledger.EffectiveEnforcement() != infra.EnforceRefuse {
		t.Errorf("ledger = %+v", ledger)
	}
	scratch, _ := cfg.ResolveObjectives("scratch")
	if scratch.RPO.Value != time.Hour || scratch.RPO.Source != "default" || scratch.AvailabilitySLO.Value != 99.0 || scratch.EffectiveEnforcement() != infra.EnforceWarn {
		t.Errorf("scratch = %+v", scratch)
	}

	// The agent that reads them is given the file.
	container := containerByName(t, objs["Deployment/test-sysadmin-agent"], "sysadmin-agent")
	if got := containerEnvMap(container)["HELPDESK_INFRA_CONFIG"]; got != "/etc/helpdesk/infrastructure.json" {
		t.Errorf("sysadmin HELPDESK_INFRA_CONFIG = %q", got)
	}
}

// What an operator is likely to get wrong in values.yaml is caught by the
// loader, loudly, and does not cost the inventory.
func TestInfrastructureObjectivesMistakesInValuesAreVisible(t *testing.T) {
	// YAML reads an unquoted 900 as a number, and a duration must be a string.
	const bad = `{"db_servers": {"a": {"name": "a", "connection_string": "host=a", "objectives": {"rpo": 900}}}}`
	cfg := loadRendered(t, renderedInfrastructure(t, render(t, "setjson:infrastructure="+bad)))
	if _, ok := cfg.DBServers["a"]; !ok {
		t.Fatal("the inventory was lost to an objective mistake")
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "a duration is a string") {
		t.Errorf("Validate = %v: the mistake should be reported with how to fix it", err)
	}
	if got, _ := cfg.ResolveObjectives("a"); got.RPO.Set || !got.RPOInvalid {
		t.Errorf("an unreadable rpo resolved as %+v", got.RPO)
	}
}

// Without the value there is no file and no pointer to one: nothing changes for
// a deployment that does not use any of this.
func TestInfrastructureAbsentByDefault(t *testing.T) {
	objs := render(t)
	if body := renderedInfrastructure(t, objs); body != "" {
		t.Errorf("an infrastructure.json was rendered with no infrastructure value: %s", body)
	}
	container := containerByName(t, objs["Deployment/test-sysadmin-agent"], "sysadmin-agent")
	if got, ok := containerEnvMap(container)["HELPDESK_INFRA_CONFIG"]; ok {
		t.Errorf("HELPDESK_INFRA_CONFIG = %q without an infrastructure value", got)
	}
}

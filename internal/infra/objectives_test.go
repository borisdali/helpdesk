package infra

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"15m":    15 * time.Minute,
		"1h30m":  90 * time.Minute,
		"0s":     0,
		"90s":    90 * time.Second,
		"1d":     24 * time.Hour,
		"2d":     48 * time.Hour,
		"1d12h":  36 * time.Hour,
		" 5m ":   5 * time.Minute,
		"7d30m":  7*24*time.Hour + 30*time.Minute,
		"1h0m0s": time.Hour,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "15", "m", "fast", "1x", "d", "99999999d", "-1d", "1d-5m", "1d+5m", "1d1d"} {
		if got, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want an error", bad, got)
		}
	}
}

// load reads a config the way a service does.
func load(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "infrastructure.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

const fleetConfig = `{
  "defaults": {"objectives": {"rpo": "1h", "rto": "4h", "availability_slo": 99.0}},
  "environments": {
    "prod":    {"description": "customer-facing", "objectives": {"rpo": "5m", "rto": "30m", "availability_slo": 99.95}},
    "staging": {"objectives": {"rpo": "1d"}},
    "dev":     {}
  },
  "db_servers": {
    "prod-orders":  {"name": "orders", "connection_string": "host=a", "environment": "prod"},
    "prod-ledger":  {"name": "ledger", "connection_string": "host=b", "environment": "prod", "objectives": {"rpo": "0s", "rto": "10m"}},
    "stage-orders": {"name": "so",     "connection_string": "host=c", "environment": "staging"},
    "dev-orders":   {"name": "do",     "connection_string": "host=d", "environment": "dev"},
    "adhoc":        {"name": "adhoc",  "connection_string": "host=e"}
  }
}`

func TestResolveObjectives_FieldByFieldWithSources(t *testing.T) {
	c := load(t, fleetConfig)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	type want struct {
		rpo, rto                 time.Duration
		rpoSrc, rtoSrc, availSrc string
		avail                    float64
	}
	for id, w := range map[string]want{
		// Everything from its environment.
		"prod-orders": {5 * time.Minute, 30 * time.Minute, "environment:prod", "environment:prod", "environment:prod", 99.95},
		// Its own rpo and rto; the availability still from the environment.
		"prod-ledger": {0, 10 * time.Minute, "database", "database", "environment:prod", 99.95},
		// The environment sets only rpo; rto and availability fall to the fleet default.
		"stage-orders": {24 * time.Hour, 4 * time.Hour, "environment:staging", "default", "default", 99.0},
		// An environment that sets nothing is all default.
		"dev-orders": {time.Hour, 4 * time.Hour, "default", "default", "default", 99.0},
		// No environment at all is the fleet default too.
		"adhoc": {time.Hour, 4 * time.Hour, "default", "default", "default", 99.0},
	} {
		got, ok := c.ResolveObjectives(id)
		if !ok {
			t.Fatalf("%s: not found", id)
		}
		if !got.RPO.Set || got.RPO.Value != w.rpo || got.RPO.Source != w.rpoSrc {
			t.Errorf("%s rpo = %+v, want %v from %s", id, got.RPO, w.rpo, w.rpoSrc)
		}
		if !got.RTO.Set || got.RTO.Value != w.rto || got.RTO.Source != w.rtoSrc {
			t.Errorf("%s rto = %+v, want %v from %s", id, got.RTO, w.rto, w.rtoSrc)
		}
		if !got.AvailabilitySLO.Set || got.AvailabilitySLO.Value != w.avail || got.AvailabilitySLO.Source != w.availSrc {
			t.Errorf("%s availability = %+v, want %v from %s", id, got.AvailabilitySLO, w.avail, w.availSrc)
		}
		if len(got.Problems) != 0 {
			t.Errorf("%s problems = %v", id, got.Problems)
		}
	}
	if got, _ := c.ResolveObjectives("adhoc"); got.Environment != "" {
		t.Errorf("adhoc environment = %q", got.Environment)
	}
	if got, _ := c.ResolveObjectives("prod-orders"); got.Environment != "prod" {
		t.Errorf("prod-orders environment = %q", got.Environment)
	}
}

// RPO 0 means "no data loss tolerated". It is a value, not an absence: it must
// not fall through to the environment's.
func TestResolveObjectives_ZeroRPOIsAValue(t *testing.T) {
	got, _ := load(t, fleetConfig).ResolveObjectives("prod-ledger")
	if !got.RPO.Set || got.RPO.Value != 0 || got.RPO.Source != "database" {
		t.Errorf("rpo = %+v", got.RPO)
	}
}

func TestResolveObjectives_NothingConfigured(t *testing.T) {
	c := load(t, `{"db_servers": {"a": {"name": "a", "connection_string": "host=a"}}}`)
	got, ok := c.ResolveObjectives("a")
	if !ok || got.RPO.Set || got.RTO.Set || got.AvailabilitySLO.Set || len(got.Problems) != 0 {
		t.Errorf("an unconfigured database has objectives or problems: %+v", got)
	}
	if _, ok := c.ResolveObjectives("missing"); ok {
		t.Error("an unknown database resolved")
	}
	var nilCfg *Config
	if _, ok := nilCfg.ResolveObjectives("a"); ok {
		t.Error("a nil config resolved")
	}
}

// What Validate reports. Each of these loads fine; the point is that Validate
// says so, and that a service can log it.
func TestValidate_ReportsWhatWouldSilentlyFallBack(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"a typo in the environment": {
			`{"environments": {"prod": {}}, "db_servers": {"a": {"environment": "prdo"}}}`,
			`environment "prdo" is not declared under environments (declared: prod)`,
		},
		"an environment with none declared": {
			`{"db_servers": {"a": {"environment": "prod"}}}`,
			`(declared: none)`,
		},
		"a bad environment name": {
			`{"environments": {"Prod Env": {}}}`,
			`"Prod Env" is not a valid environment name`,
		},
		"a negative rpo": {
			`{"defaults": {"objectives": {"rpo": "-5m"}}}`,
			`defaults.objectives: rpo must not be negative`,
		},
		"a zero rto": {
			`{"environments": {"prod": {"objectives": {"rto": "0s"}}}}`,
			`environments.prod.objectives: rto must be greater than zero`,
		},
		"availability above 100": {
			`{"db_servers": {"a": {"objectives": {"availability_slo": 101}}}}`,
			`db_servers.a.objectives: availability_slo is a percentage`,
		},
		"availability of zero": {
			`{"db_servers": {"a": {"objectives": {"availability_slo": 0}}}}`,
			`availability_slo is a percentage`,
		},
		"a number for a duration": {
			`{"defaults": {"objectives": {"rpo": 900}}}`,
			`a duration is a string`,
		},
		"an unreadable duration": {
			`{"defaults": {"objectives": {"rto": "soon"}}}`,
			`invalid duration "soon"`,
		},
		"a string for a percentage": {
			`{"defaults": {"objectives": {"availability_slo": "99.9"}}}`,
			`a percentage is a number`,
		},
	} {
		err := load(t, tc.body).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	err := load(t, `{
	  "defaults": {"objectives": {"rto": "0s"}},
	  "db_servers": {"a": {"environment": "nope"}, "b": {"environment": "nada"}}
	}`).Validate()
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"rto must be greater than zero", `"nope"`, `"nada"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// The reason Load is lenient: most services treat a config that fails to load
// as "no infrastructure config" and carry on, so a mistake in an objective must
// not cost them every target, tag and approval rule.
func TestLoad_ABadObjectiveNeverCostsTheInventory(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"rpo": 900, "availability_slo": "high"}},
	  "environments": {"prod": {"objectives": {"rto": "soon"}}},
	  "db_servers": {
	    "orders": {"name": "orders", "connection_string": "host=a port=5432 dbname=o", "environment": "prdo", "tags": ["production"],
	               "objectives": {"rpo": "-1h"}, "approval_override_roles": ["dba"]}
	  },
	  "vms": {"vm1": {"name": "vm1", "address": "10.0.0.1"}}
	}`)
	db, key, ok := c.FindDBByConnStr("host=a port=5432 dbname=o")
	if !ok || key != "orders" || len(db.Tags) != 1 || db.Tags[0] != "production" || len(db.ApprovalOverrideRoles) != 1 {
		t.Fatalf("the inventory was damaged: %+v %q %v", db, key, ok)
	}
	if len(c.VMs) != 1 {
		t.Errorf("vms = %v", c.VMs)
	}
	if c.Validate() == nil {
		t.Error("Validate found nothing wrong")
	}
}

// An invalid value blocks its own objective. It resolves as unset with the
// reason; it never becomes the next level's value, which would pass a typo off
// as the fleet default.
func TestResolveObjectives_AnInvalidValueBlocksItsObjective(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"rpo": "1h", "rto": "4h"}},
	  "environments": {"prod": {"objectives": {"rpo": "5m", "rto": "30m"}}},
	  "db_servers": {
	    "bad-rpo": {"environment": "prod", "objectives": {"rpo": "soon"}},
	    "neg-rto": {"environment": "prod", "objectives": {"rto": "0s"}}
	  }
	}`)

	got, _ := c.ResolveObjectives("bad-rpo")
	if got.RPO.Set {
		t.Errorf("an unreadable rpo fell through to %+v", got.RPO)
	}
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "db_servers.bad-rpo.objectives") || !strings.Contains(got.Problems[0], `"soon"`) {
		t.Errorf("problems = %v", got.Problems)
	}
	// The other objective is unaffected.
	if !got.RTO.Set || got.RTO.Value != 30*time.Minute || got.RTO.Source != "environment:prod" {
		t.Errorf("rto = %+v", got.RTO)
	}

	got, _ = c.ResolveObjectives("neg-rto")
	if got.RTO.Set || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "rto must be greater than zero") {
		t.Errorf("rto = %+v, problems = %v", got.RTO, got.Problems)
	}
	if !got.RPO.Set || got.RPO.Source != "environment:prod" {
		t.Errorf("rpo = %+v", got.RPO)
	}
}

// A database in an environment that is not declared gets only what it sets
// itself: the environment's values do not exist, and the fleet default is not
// a substitute for them.
func TestResolveObjectives_AnUndeclaredEnvironmentGetsOnlyTheDatabasesOwn(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"rpo": "1h", "rto": "4h"}},
	  "environments": {"prod": {"objectives": {"rpo": "5m"}}},
	  "db_servers": {
	    "typo":    {"environment": "prdo"},
	    "typo-own": {"environment": "prdo", "objectives": {"rto": "15m"}}
	  }
	}`)
	got, ok := c.ResolveObjectives("typo")
	if !ok || got.RPO.Set || got.RTO.Set || got.AvailabilitySLO.Set {
		t.Errorf("a typo'd environment fell back: %+v", got)
	}
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], `environment "prdo" is not declared`) {
		t.Errorf("problems = %v", got.Problems)
	}
	got, _ = c.ResolveObjectives("typo-own")
	if !got.RTO.Set || got.RTO.Source != "database" || got.RPO.Set {
		t.Errorf("own objective lost or default leaked: %+v", got)
	}
}

// A Config built in code, not loaded, can name an environment it never declared.
func TestResolveObjectives_ConfigBuiltInCode(t *testing.T) {
	five := Duration{Duration: 5 * time.Minute}
	c := &Config{
		Defaults:  &Defaults{Objectives: Objectives{RPO: &five}},
		DBServers: map[string]DBServer{"a": {Environment: "ghost"}},
	}
	if got, _ := c.ResolveObjectives("a"); got.RPO.Set || len(got.Problems) == 0 {
		t.Errorf("got %+v", got)
	}
}

// Nothing about the objectives constrains a config that predates them.
func TestLoad_ExistingConfigsAreUnaffected(t *testing.T) {
	c, err := Load("../../deploy/docker-compose/infrastructure.json.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for id := range c.DBServers {
		got, _ := c.ResolveObjectives(id)
		if got.RPO.Set || got.RTO.Set || got.Environment != "" || len(got.Problems) != 0 {
			t.Errorf("%s: %+v", id, got)
		}
	}
	// Tags are still just tags: an environment name is not implied by one.
	c = load(t, `{"db_servers": {"a": {"tags": ["production"]}}}`)
	if got, _ := c.ResolveObjectives("a"); got.Environment != "" {
		t.Errorf("a tag became an environment: %+v", got)
	}
}

func TestDuration_ReadsDaysFromJSON(t *testing.T) {
	c := load(t, `{"defaults": {"objectives": {"rpo": "1d12h"}}}`)
	if got := c.Defaults.Objectives.RPO.Duration; got != 36*time.Hour {
		t.Errorf("rpo = %v", got)
	}
}

// A config that is written back out (some tools re-serialise it) reads back the
// same.
func TestConfig_JSONRoundTrip(t *testing.T) {
	c := load(t, fleetConfig)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	for id := range c.DBServers {
		a, _ := c.ResolveObjectives(id)
		b, _ := back.ResolveObjectives(id)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: %+v became %+v", id, a, b)
		}
	}
}

func TestLogProblems(t *testing.T) {
	var lines []string
	log := func(msg string, args ...any) {
		lines = append(lines, msg+" "+args[1].(string))
	}
	if n := load(t, fleetConfig).LogProblems(log); n != 0 || len(lines) != 0 {
		t.Errorf("a valid config logged %d line(s): %v", n, lines)
	}
	c := load(t, `{"defaults": {"objectives": {"rto": "0s"}}, "db_servers": {"a": {"environment": "nope"}}}`)
	if n := c.LogProblems(log); n != 2 || len(lines) != 2 {
		t.Errorf("logged %d line(s): %v", n, lines)
	}
	if !strings.Contains(lines[0]+lines[1], `environment "nope"`) {
		t.Errorf("lines = %v", lines)
	}
	var nilCfg *Config
	if nilCfg.LogProblems(log) != 0 {
		t.Error("a nil config logged")
	}
}

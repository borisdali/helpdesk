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

// An invalid value must not be written back out as a valid one: "0s" would turn
// a typo into an rpo of zero, "no data loss tolerated".
func TestMarshal_RefusesInvalidValues(t *testing.T) {
	c := load(t, `{"defaults": {"objectives": {"rpo": "soon", "availability_slo": "high"}}}`)
	if raw, err := json.Marshal(c); err == nil {
		t.Errorf("an invalid config was written out: %s", raw)
	}
	if raw, err := json.Marshal(c.Defaults.Objectives.RPO); err == nil {
		t.Errorf("an invalid duration was written as %s", raw)
	}
	if raw, err := json.Marshal(c.Defaults.Objectives.AvailabilitySLO); err == nil {
		t.Errorf("an invalid percentage was written as %s", raw)
	}
	// Valid values, including zero, are written.
	zero := Duration{}
	if raw, err := json.Marshal(zero); err != nil || string(raw) != `"0s"` {
		t.Errorf("a valid zero duration: %s, %v", raw, err)
	}
}

// Without a defaults block, an objective nobody set is simply unset, with no
// problem to report: "not configured" is not a mistake.
func TestResolveObjectives_NoDefaultsBlock(t *testing.T) {
	c := load(t, `{
	  "environments": {"prod": {"objectives": {"rpo": "5m"}}},
	  "db_servers": {"a": {"environment": "prod"}, "b": {}}
	}`)
	got, _ := c.ResolveObjectives("a")
	if !got.RPO.Set || got.RPO.Source != "environment:prod" || got.RTO.Set || got.AvailabilitySLO.Set || len(got.Problems) != 0 {
		t.Errorf("a = %+v", got)
	}
	got, _ = c.ResolveObjectives("b")
	if got.RPO.Set || got.RTO.Set || len(got.Problems) != 0 {
		t.Errorf("b = %+v", got)
	}
}

const policyConfig = `{
  "defaults": {"objectives": {"rpo": "1h", "enforcement": "warn"}},
  "environments": {
    "prod":    {"objectives": {"rpo": "5m", "recovery_preference": "data_first", "enforcement": "acknowledge"}},
    "staging": {"objectives": {"recovery_preference": "availability_first"}},
    "dev":     {}
  },
  "db_servers": {
    "prod-orders": {"environment": "prod"},
    "prod-ledger": {"environment": "prod", "objectives": {"enforcement": "refuse"}},
    "prod-cache":  {"environment": "prod", "objectives": {"recovery_preference": "availability_first"}},
    "stage":       {"environment": "staging"},
    "dev":         {"environment": "dev"},
    "adhoc":       {}
  }
}`

func TestResolveObjectives_PreferenceAndEnforcement(t *testing.T) {
	c := load(t, policyConfig)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for id, w := range map[string]struct {
		pref, prefSrc, enf, enfSrc, effective string
	}{
		"prod-orders": {"data_first", "environment:prod", "acknowledge", "environment:prod", "acknowledge"},
		// The database's own enforcement; the preference still from the environment.
		"prod-ledger": {"data_first", "environment:prod", "refuse", "database", "refuse"},
		// Its own preference; the enforcement still from the environment.
		"prod-cache": {"availability_first", "database", "acknowledge", "environment:prod", "acknowledge"},
		// The environment sets only a preference; enforcement falls to the fleet default.
		"stage": {"availability_first", "environment:staging", "warn", "default", "warn"},
		// Nothing set above the default: no preference at all.
		"dev":   {"", "", "warn", "default", "warn"},
		"adhoc": {"", "", "warn", "default", "warn"},
	} {
		got, _ := c.ResolveObjectives(id)
		if got.RecoveryPreference.Value != w.pref || got.RecoveryPreference.Source != w.prefSrc || got.RecoveryPreference.Set != (w.pref != "") {
			t.Errorf("%s preference = %+v, want %q from %q", id, got.RecoveryPreference, w.pref, w.prefSrc)
		}
		if got.Enforcement.Value != w.enf || got.Enforcement.Source != w.enfSrc {
			t.Errorf("%s enforcement = %+v, want %q from %q", id, got.Enforcement, w.enf, w.enfSrc)
		}
		if got.EffectiveEnforcement() != w.effective {
			t.Errorf("%s effective enforcement = %s, want %s", id, got.EffectiveEnforcement(), w.effective)
		}
		if len(got.Problems) != 0 {
			t.Errorf("%s problems = %v", id, got.Problems)
		}
	}
}

// With nothing configured the behaviour is what it was before enforcement
// existed: flag, never block.
func TestEffectiveEnforcement_DefaultsToWarn(t *testing.T) {
	c := load(t, `{"db_servers": {"a": {}}}`)
	got, _ := c.ResolveObjectives("a")
	if got.Enforcement.Set || got.EffectiveEnforcement() != EnforceWarn {
		t.Errorf("enforcement = %+v, effective %s", got.Enforcement, got.EffectiveEnforcement())
	}
}

func TestValidate_PreferenceAndEnforcement(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"an unknown enforcement": {
			`{"defaults": {"objectives": {"enforcement": "block"}}}`,
			`enforcement must be one of warn, acknowledge, refuse, got "block"`,
		},
		"an unknown preference": {
			`{"environments": {"prod": {"objectives": {"recovery_preference": "fast"}}}}`,
			`recovery_preference must be one of availability_first, data_first, got "fast"`,
		},
		// Exact match: "Refuse" is not "refuse". A near miss is an error, not a guess.
		"a case near miss": {
			`{"db_servers": {"a": {"objectives": {"enforcement": "Refuse"}}}}`,
			`got "Refuse"`,
		},
		"not a string": {
			`{"db_servers": {"a": {"objectives": {"recovery_preference": 5}}}}`,
			`expected a string, not 5`,
		},
		"an empty string": {
			`{"db_servers": {"a": {"objectives": {"enforcement": ""}}}}`,
			`got ""`,
		},
	} {
		err := load(t, tc.body).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

// A typo in enforcement must not quietly weaken the check to warn.
func TestResolveObjectives_InvalidEnforcementFailsTowardCaution(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"enforcement": "warn"}},
	  "db_servers": {"typo": {"objectives": {"enforcement": "refuze"}}, "fine": {}}
	}`)
	got, _ := c.ResolveObjectives("typo")
	if got.Enforcement.Set || !got.EnforcementInvalid {
		t.Errorf("an invalid enforcement fell through or was accepted: %+v", got)
	}
	if got.EffectiveEnforcement() != EnforceAcknowledge {
		t.Errorf("effective = %s, want acknowledge (not the default's warn)", got.EffectiveEnforcement())
	}
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], `got "refuze"`) {
		t.Errorf("problems = %v", got.Problems)
	}
	if fine, _ := c.ResolveObjectives("fine"); fine.EnforcementInvalid || fine.EffectiveEnforcement() != EnforceWarn {
		t.Errorf("a typo elsewhere leaked: %+v", fine)
	}
}

// A misspelt environment name must not weaken a strict environment to warn: the
// database cannot tell which enforcement its real environment would have set.
func TestEffectiveEnforcement_UndeclaredEnvironmentFailsTowardCaution(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"enforcement": "warn"}},
	  "environments": {"prod": {"objectives": {"enforcement": "refuse"}}},
	  "db_servers": {
	    "typo":         {"environment": "prdo"},
	    "typo-own":     {"environment": "prdo", "objectives": {"enforcement": "refuse"}},
	    "typo-own-warn": {"environment": "prdo", "objectives": {"enforcement": "warn"}},
	    "prod":         {"environment": "prod"},
	    "none":         {}
	  }
	}`)
	for id, want := range map[string]string{
		// The default's warn is deliberately not used for a database whose
		// environment is unknown, and prod's refuse cannot be known: acknowledge.
		"typo": EnforceAcknowledge,
		// What the database says itself is honoured, whatever the environment.
		"typo-own":      EnforceRefuse,
		"typo-own-warn": EnforceWarn,
		// A real environment, and no environment at all, are unaffected.
		"prod": EnforceRefuse,
		"none": EnforceWarn,
	} {
		got, _ := c.ResolveObjectives(id)
		if got.EffectiveEnforcement() != want {
			t.Errorf("%s: effective = %s, want %s (%+v)", id, got.EffectiveEnforcement(), want, got)
		}
	}
	if got, _ := c.ResolveObjectives("typo"); len(got.Problems) == 0 {
		t.Error("the unknown environment was not reported")
	}
}

// An invalid enforcement at the environment or fleet level reaches every
// database that inherits it, and a database's own valid setting still wins.
func TestEffectiveEnforcement_InvalidInheritedValue(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"enforcement": "warnn"}},
	  "environments": {"prod": {"objectives": {"enforcement": "refuze"}}, "dev": {}},
	  "db_servers": {
	    "prod-a":   {"environment": "prod"},
	    "prod-own": {"environment": "prod", "objectives": {"enforcement": "refuse"}},
	    "dev-a":    {"environment": "dev"},
	    "bare":     {}
	  }
	}`)
	for id, want := range map[string]string{
		"prod-a":   EnforceAcknowledge, // the environment's typo
		"prod-own": EnforceRefuse,      // its own valid setting wins over it
		"dev-a":    EnforceAcknowledge, // the fleet default's typo
		"bare":     EnforceAcknowledge,
	} {
		got, _ := c.ResolveObjectives(id)
		if got.EffectiveEnforcement() != want {
			t.Errorf("%s: effective = %s, want %s (%+v)", id, got.EffectiveEnforcement(), want, got)
		}
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `got "refuze"`) || !strings.Contains(err.Error(), `got "warnn"`) {
		t.Errorf("Validate = %v", err)
	}
}

// An invalid preference, unlike enforcement, simply resolves as unset: with no
// preference nothing is ranked, which is the pre-preference behaviour.
func TestResolveObjectives_InvalidPreferenceBlocksItself(t *testing.T) {
	c := load(t, `{
	  "environments": {"prod": {"objectives": {"recovery_preference": "data_first"}}},
	  "db_servers": {"a": {"environment": "prod", "objectives": {"recovery_preference": "quick"}}}
	}`)
	got, _ := c.ResolveObjectives("a")
	if got.RecoveryPreference.Set || len(got.Problems) != 1 {
		t.Errorf("an invalid preference fell through to the environment's: %+v", got)
	}
}

func TestChoice_MarshalAndRoundTrip(t *testing.T) {
	c := load(t, policyConfig)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for id := range c.DBServers {
		a, _ := c.ResolveObjectives(id)
		b, _ := back.ResolveObjectives(id)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: %+v became %+v", id, a, b)
		}
	}
	bad := load(t, `{"defaults": {"objectives": {"enforcement": 5}}}`)
	if raw, err := json.Marshal(bad); err == nil {
		t.Errorf("an invalid setting was written out: %s", raw)
	}
}

// RPOInvalid separates "there is no rpo" from "there is one and it cannot be
// read or found": only the second is something a caller must not treat as
// nothing to check.
func TestResolveObjectives_RPOInvalid(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"rpo": "soon"}},
	  "environments": {
	    "bad-env":  {"objectives": {"rpo": "-5m"}},
	    "ok-env":   {"objectives": {"rpo": "5m"}},
	    "rto-typo": {"objectives": {"rto": "0s"}},
	    "empty":    {}
	  },
	  "db_servers": {
	    "own-bad":      {"environment": "ok-env", "objectives": {"rpo": "later"}},
	    "env-bad":      {"environment": "bad-env"},
	    "default-bad":  {"environment": "empty"},
	    "fine":         {"environment": "ok-env"},
	    "own-fixes":    {"environment": "bad-env", "objectives": {"rpo": "10m"}},
	    "unknown-env":  {"environment": "prdo"},
	    "unknown-own":  {"environment": "prdo", "objectives": {"rpo": "10m"}},
	    "rto-only-bad": {"environment": "rto-typo", "objectives": {"rpo": "1m"}}
	  }
	}`)
	for id, want := range map[string]bool{
		"own-bad":      true,  // its own value cannot be read, and does not fall through to ok-env's
		"env-bad":      true,  // inherited from its environment
		"default-bad":  true,  // inherited from the fleet default
		"fine":         false, // 5m from ok-env
		"own-fixes":    false, // a valid rpo of its own wins over a bad environment's
		"unknown-env":  true,  // cannot tell what its environment would have required
		"unknown-own":  false, // but one of its own is enough
		"rto-only-bad": false, // a problem with another setting is not an rpo problem
	} {
		got, _ := c.ResolveObjectives(id)
		if got.RPOInvalid != want {
			t.Errorf("%s: RPOInvalid = %v, want %v (rpo %+v, problems %v)", id, got.RPOInvalid, want, got.RPO, got.Problems)
		}
		if got.RPOInvalid && got.RPO.Set {
			t.Errorf("%s: an rpo that cannot be trusted was also resolved: %+v", id, got.RPO)
		}
	}

	// No rpo at all is not an invalid one.
	c = load(t, `{"environments": {"dev": {}}, "db_servers": {"none": {"environment": "dev"}, "bare": {}}}`)
	for _, id := range []string{"none", "bare"} {
		if got, _ := c.ResolveObjectives(id); got.RPOInvalid || got.RPO.Set {
			t.Errorf("%s: %+v", id, got)
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

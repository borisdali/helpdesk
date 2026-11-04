package infra

import (
	"os"
	"path/filepath"
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
	for _, bad := range []string{"", "  ", "15", "m", "fast", "1x", "d", "99999999d", "-1d"} {
		if got, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want an error", bad, got)
		}
	}
}

// load parses a config the way Load does, from text.
func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "infrastructure.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
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
	c, err := load(t, fleetConfig)
	if err != nil {
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
	c, err := load(t, fleetConfig)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := c.ResolveObjectives("prod-ledger")
	if !got.RPO.Set || got.RPO.Value != 0 || got.RPO.Source != "database" {
		t.Errorf("rpo = %+v", got.RPO)
	}
}

func TestResolveObjectives_NothingConfigured(t *testing.T) {
	c, err := load(t, `{"db_servers": {"a": {"name": "a", "connection_string": "host=a"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.ResolveObjectives("a")
	if !ok || got.RPO.Set || got.RTO.Set || got.AvailabilitySLO.Set {
		t.Errorf("an unconfigured database has objectives: %+v", got)
	}
	if _, ok := c.ResolveObjectives("missing"); ok {
		t.Error("an unknown database resolved")
	}
	var nilCfg *Config
	if _, ok := nilCfg.ResolveObjectives("a"); ok {
		t.Error("a nil config resolved")
	}
}

func TestLoad_RejectsWhatWouldSilentlyFallBack(t *testing.T) {
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
			`db_servers.a.objectives: availability_slo`,
		},
		"availability of zero": {
			`{"db_servers": {"a": {"objectives": {"availability_slo": 0}}}}`,
			`availability_slo`,
		},
		"a number for a duration": {
			`{"defaults": {"objectives": {"rpo": 900}}}`,
			`a duration is a string`,
		},
		"an unreadable duration": {
			`{"defaults": {"objectives": {"rto": "soon"}}}`,
			`invalid duration "soon"`,
		},
	} {
		if _, err := load(t, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	_, err := load(t, `{
	  "defaults": {"objectives": {"rto": "0s"}},
	  "db_servers": {"a": {"environment": "nope"}, "b": {"environment": "nada"}}
	}`)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"rto must be greater than zero", `"nope"`, `"nada"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// Nothing about the objectives constrains a config that predates them.
func TestLoad_ExistingConfigsAreUnaffected(t *testing.T) {
	for _, p := range []string{"../../deploy/docker-compose/infrastructure.json.example"} {
		c, err := Load(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for id := range c.DBServers {
			got, _ := c.ResolveObjectives(id)
			if got.RPO.Set || got.RTO.Set || got.Environment != "" {
				t.Errorf("%s/%s: %+v", p, id, got)
			}
		}
	}
	// Tags are still just tags: an environment name is not implied by one.
	c, err := load(t, `{"db_servers": {"a": {"tags": ["production"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.ResolveObjectives("a"); got.Environment != "" {
		t.Errorf("a tag became an environment: %+v", got)
	}
}

func TestDuration_ReadsDaysFromJSON(t *testing.T) {
	c, err := load(t, `{"defaults": {"objectives": {"rpo": "1d12h"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Defaults.Objectives.RPO.Duration; got != 36*time.Hour {
		t.Errorf("rpo = %v", got)
	}
}

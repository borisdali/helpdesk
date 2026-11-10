package infra

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestComputeBudget(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name          string
		slo           float64
		window        time.Duration
		rto           time.Duration
		rtoSet        bool
		allowed       time.Duration
		tolerated     int
		contradictory bool
		share         float64
	}{
		{"99.9% over 30d is 43m12s, and a 30m rto uses most of it", 99.9, 30 * day, 30 * time.Minute, true, 43*time.Minute + 12*time.Second, 1, false, 30.0 / 43.2},
		{"99.99% over 30d is 4m19.2s, so a 30m rto contradicts it", 99.99, 30 * day, 30 * time.Minute, true, 4*time.Minute + 19200*time.Millisecond, 0, true, 30.0 / 4.32},
		{"a 5m rto fits 8 times in 43m12s", 99.9, 30 * day, 5 * time.Minute, true, 43*time.Minute + 12*time.Second, 8, false, 5.0 / 43.2},
		{"an rto exactly the budget fits once and is not a contradiction", 99.9, 30 * day, 43*time.Minute + 12*time.Second, true, 43*time.Minute + 12*time.Second, 1, false, 1},
		{"an rto one second over the budget is a contradiction", 99.9, 30 * day, 43*time.Minute + 13*time.Second, true, 43*time.Minute + 12*time.Second, 0, true, 2593.0 / 2592},
		{"99% over 7d", 99, 7 * day, time.Hour, true, time.Hour + 40*time.Minute + 48*time.Second, 1, false, 3600.0 / 6048},
		{"100% allows no downtime: no recovery fits", 100, 30 * day, time.Minute, true, 0, 0, true, 0},
		{"no rto: the budget is known, the recoveries it absorbs are not", 99.9, 30 * day, 0, false, 43*time.Minute + 12*time.Second, 0, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := ComputeBudget(tc.slo, tc.window, "environment:prod", tc.rto, tc.rtoSet)
			if !b.Computed {
				t.Fatalf("not computed: %s", b.Detail)
			}
			if b.Allowed != tc.allowed {
				t.Errorf("Allowed = %v, want %v", b.Allowed, tc.allowed)
			}
			if b.Tolerated != tc.tolerated || b.Contradictory != tc.contradictory {
				t.Errorf("Tolerated = %d, Contradictory = %v; want %d, %v (%s)", b.Tolerated, b.Contradictory, tc.tolerated, tc.contradictory, b.Detail)
			}
			if tc.rtoSet && tc.allowed > 0 && math.Abs(b.RTOShare-tc.share) > 1e-6 {
				t.Errorf("RTOShare = %v, want %v", b.RTOShare, tc.share)
			}
			if b.RTOKnown != tc.rtoSet {
				t.Errorf("RTOKnown = %v", b.RTOKnown)
			}
		})
	}
}

// The statements people read, pinned: they are shown at the gate and in logs.
func TestComputeBudget_Statements(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		slo    float64
		window time.Duration
		src    string
		rto    time.Duration
		rtoSet bool
		want   string
	}{
		{99.9, 30 * day, "environment:prod", 30 * time.Minute, true,
			"availability 99.9% over 30d allows 43m12s of downtime per window; rto 30m uses 69% of it: tolerates 1 recovery per window at the rto"},
		{99.9, 30 * day, "environment:prod", 5 * time.Minute, true,
			"availability 99.9% over 30d allows 43m12s of downtime per window; rto 5m uses 12% of it: tolerates 8 recoveries per window at the rto"},
		{99.99, 30 * day, SourceBuiltin, 30 * time.Minute, true,
			"availability 99.99% over 30d (default window) allows 4m19s of downtime per window; rto 30m is longer than the whole budget: one recovery that takes the full rto breaks the SLO"},
		{99.9, 30 * day, "default", 0, false,
			"availability 99.9% over 30d allows 43m12s of downtime per window; no rto is set, so the recoveries it absorbs are not computed"},
		{100, 7 * day, "database", 15 * time.Minute, true,
			"availability 100% over 7d allows no downtime, so no recovery fits (rto 15m)"},
	}
	for _, tc := range tests {
		if got := ComputeBudget(tc.slo, tc.window, tc.src, tc.rto, tc.rtoSet).Detail; got != tc.want {
			t.Errorf("slo %v:\n got  %s\n want %s", tc.slo, got, tc.want)
		}
	}
}

func TestComputeBudget_RefusesNonsense(t *testing.T) {
	for _, tc := range []struct {
		slo    float64
		window time.Duration
	}{{0, 30 * 24 * time.Hour}, {-1, time.Hour}, {100.1, time.Hour}, {99.9, 0}, {99.9, -time.Hour}, {math.NaN(), time.Hour}} {
		b := ComputeBudget(tc.slo, tc.window, "x", time.Minute, true)
		if b.Computed || b.Contradictory || b.Tolerated != 0 {
			t.Errorf("(%v, %v) computed a budget: %+v", tc.slo, tc.window, b)
		}
	}
}

func TestFormatSpan(t *testing.T) {
	for in, want := range map[time.Duration]string{
		30 * 24 * time.Hour:                    "30d",
		7 * 24 * time.Hour:                     "7d",
		36 * time.Hour:                         "1d12h",
		25*time.Hour + 30*time.Minute:          "1d1h30m",
		time.Hour:                              "1h",
		90 * time.Minute:                       "1h30m",
		30 * time.Minute:                       "30m",
		43*time.Minute + 12*time.Second:        "43m12s",
		4*time.Minute + 19200*time.Millisecond: "4m19s",
		time.Minute:                            "1m",
		45 * time.Second:                       "45s",
		2592 * time.Millisecond:                "2.592s",
		0:                                      "0s",
	} {
		if got := FormatSpan(in); got != want {
			t.Errorf("FormatSpan(%v) = %q, want %q", in, got, want)
		}
	}
}

// --- the window as a setting ---

func TestAvailabilityWindow_ResolvesFieldByFieldWithSources(t *testing.T) {
	c := load(t, fleetConfig)
	// prod sets a 90d window; the databases below take it from the environment.
	got, _ := c.ResolveObjectives("prod-orders")
	if !got.AvailabilityWindow.Set || got.AvailabilityWindow.Value != 90*24*time.Hour || got.AvailabilityWindow.Source != "environment:prod" {
		t.Errorf("prod-orders window = %+v", got.AvailabilityWindow)
	}
	// Nothing sets a window for staging: unset as declared; Budget uses the default and says so.
	got, _ = c.ResolveObjectives("stage-orders")
	if got.AvailabilityWindow.Set {
		t.Errorf("stage-orders window = %+v, want unset", got.AvailabilityWindow)
	}
	if b := got.Budget(); b.Window != DefaultAvailabilityWindow || b.WindowSource != SourceBuiltin {
		t.Errorf("stage-orders budget window = %v from %q, want the built-in default", b.Window, b.WindowSource)
	}
}

func TestAvailabilityWindow_Validate(t *testing.T) {
	for _, tc := range []struct{ name, cfg, want string }{
		{"a bare number is not a duration", `{"defaults": {"objectives": {"availability_window": 30}}}`, "defaults.objectives: a duration is a string"},
		{"unreadable", `{"defaults": {"objectives": {"availability_window": "a month"}}}`, "defaults.objectives: invalid duration"},
		{"zero", `{"environments": {"prod": {"objectives": {"availability_window": "0s"}}}}`, "environments.prod.objectives: availability_window must be greater than zero"},
		{"negative", `{"db_servers": {"a": {"objectives": {"availability_window": "-5m"}}}}`, "db_servers.a.objectives: availability_window must be greater than zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := load(t, tc.cfg).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if err := load(t, `{"defaults": {"objectives": {"availability_slo": 99.9, "availability_window": "30d"}}}`).Validate(); err != nil {
		t.Errorf("a valid window failed: %v", err)
	}
}

func TestAvailabilityWindow_RoundTripsThroughJSON(t *testing.T) {
	c := load(t, `{"defaults": {"objectives": {"availability_slo": 99.9, "availability_window": "1d12h"}}}`)
	b, err := json.Marshal(c.Defaults.Objectives)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"availability_window":"36h0m0s"`) {
		t.Errorf("marshalled = %s", b)
	}
}

// --- Budget on a resolved database ---

func TestBudget_NoSLO(t *testing.T) {
	c := load(t, `{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"rto": "30m"}}}}`)
	res, _ := c.ResolveObjectives("a")
	b := res.Budget()
	if b.Computed || b.Contradictory || !strings.Contains(b.Detail, "no availability_slo") {
		t.Errorf("budget = %+v", b)
	}
}

// A typo must never become the default window or "no objective".
func TestBudget_InvalidSettingsComputeNothing(t *testing.T) {
	t.Run("an unreadable window is not replaced by the default", func(t *testing.T) {
		c := load(t, `{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"availability_slo": 99.9, "availability_window": "a month", "rto": "30m"}}}}`)
		res, _ := c.ResolveObjectives("a")
		b := res.Budget()
		if b.Computed || !res.AvailabilityWindowInvalid || !strings.Contains(b.Detail, "availability_window cannot be trusted") {
			t.Errorf("budget = %+v, res.AvailabilityWindowInvalid = %v", b, res.AvailabilityWindowInvalid)
		}
		if b.Window == DefaultAvailabilityWindow {
			t.Error("the default window was applied over an unreadable one")
		}
	})
	t.Run("an unreadable slo is not no objective", func(t *testing.T) {
		c := load(t, `{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"availability_slo": "high", "rto": "30m"}}}}`)
		res, _ := c.ResolveObjectives("a")
		b := res.Budget()
		if b.Computed || !res.AvailabilitySLOInvalid || !strings.Contains(b.Detail, "cannot be trusted") {
			t.Errorf("budget = %+v", b)
		}
	})
	t.Run("an undeclared environment: what it would have set is unknown", func(t *testing.T) {
		c := load(t, `{"defaults": {"objectives": {"availability_slo": 99.0, "availability_window": "7d"}}, "db_servers": {"a": {"connection_string": "host=a", "environment": "prdo", "objectives": {"rto": "30m"}}}}`)
		res, _ := c.ResolveObjectives("a")
		if b := res.Budget(); b.Computed || !res.AvailabilitySLOInvalid {
			t.Errorf("the fleet default slo was used for a database in an undeclared environment: %+v", b)
		}
		// With its own slo, the window it might have had from its environment is unknown too.
		c = load(t, `{"db_servers": {"a": {"connection_string": "host=a", "environment": "prdo", "objectives": {"availability_slo": 99.9, "rto": "30m"}}}}`)
		res, _ = c.ResolveObjectives("a")
		if b := res.Budget(); b.Computed || !res.AvailabilityWindowInvalid {
			t.Errorf("the default window was applied in an undeclared environment: %+v", b)
		}
		// With both its own, it is computed.
		c = load(t, `{"db_servers": {"a": {"connection_string": "host=a", "environment": "prdo", "objectives": {"availability_slo": 99.9, "availability_window": "30d", "rto": "30m"}}}}`)
		res, _ = c.ResolveObjectives("a")
		if b := res.Budget(); !b.Computed || b.Tolerated != 1 {
			t.Errorf("budget = %+v", b)
		}
	})
}

// The general rule, for the window: an invalid value blocks its objective, it
// does not fall through to the next level, not even to a valid one.
func TestBudget_AnInvalidWindowDoesNotFallThroughToTheEnvironments(t *testing.T) {
	c := load(t, `{
	  "environments": {"prod": {"objectives": {"availability_slo": 99.9, "availability_window": "30d", "rto": "30m"}}},
	  "db_servers": {
	    "bad":  {"connection_string": "host=a", "environment": "prod", "objectives": {"availability_window": "a month"}},
	    "good": {"connection_string": "host=b", "environment": "prod", "objectives": {"availability_window": "7d"}}
	  }
	}`)
	bad, _ := c.ResolveObjectives("bad")
	if b := bad.Budget(); b.Computed || !bad.AvailabilityWindowInvalid || bad.AvailabilityWindow.Set {
		t.Errorf("the database's unreadable window fell through to its environment's: %+v / %+v", bad.AvailabilityWindow, b)
	}
	// The slo still resolves from the environment: only the window is blocked.
	if !bad.AvailabilitySLO.Set || bad.AvailabilitySLO.Source != "environment:prod" {
		t.Errorf("the slo was disturbed: %+v", bad.AvailabilitySLO)
	}
	good, _ := c.ResolveObjectives("good")
	if b := good.Budget(); !b.Computed || b.Window != 7*24*time.Hour || b.WindowSource != "database" {
		t.Errorf("a database's own valid window should win over its environment's: %+v", b)
	}
}

func TestBudget_ReportsTheDefaultWindowAsADefault(t *testing.T) {
	c := load(t, `{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"availability_slo": 99.9, "rto": "30m"}}}}`)
	res, _ := c.ResolveObjectives("a")
	b := res.Budget()
	if !b.Computed || b.WindowSource != SourceBuiltin || !strings.Contains(b.Detail, "over 30d (default window)") {
		t.Errorf("budget = %+v", b)
	}
}

// --- availability_enforcement: its own setting ---

// The reason it is separate: a config written for v0.34 that says `enforcement:
// refuse` means "refuse a restore that loses too much data". It must not start
// refusing restores for a budget it never mentioned.
func TestAvailabilityEnforcement_IsIndependentOfTheRPOsEnforcement(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"enforcement": "refuse"}},
	  "environments": {"prod": {"objectives": {"enforcement": "acknowledge"}}},
	  "db_servers": {
	    "strict-rpo":  {"connection_string": "host=a", "environment": "prod", "objectives": {"enforcement": "refuse"}},
	    "both":        {"connection_string": "host=b", "environment": "prod", "objectives": {"enforcement": "refuse", "availability_enforcement": "acknowledge"}},
	    "budget-only": {"connection_string": "host=c", "objectives": {"availability_enforcement": "refuse"}}
	  }
	}`)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]struct{ rpo, budget string }{
		// An existing refuse does not reach the budget.
		"strict-rpo": {EnforceRefuse, EnforceWarn},
		// Each says its own.
		"both": {EnforceRefuse, EnforceAcknowledge},
		// (The fleet default still sets the rpo's.)
		"budget-only": {EnforceRefuse, EnforceRefuse},
	} {
		res, _ := c.ResolveObjectives(id)
		if got := res.EffectiveEnforcement(); got != want.rpo {
			t.Errorf("%s: rpo enforcement = %q, want %q", id, got, want.rpo)
		}
		if got := res.EffectiveAvailabilityEnforcement(); got != want.budget {
			t.Errorf("%s: availability enforcement = %q, want %q", id, got, want.budget)
		}
	}
}

func TestAvailabilityEnforcement_ResolvesFieldByFieldWithSources(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"availability_enforcement": "warn"}},
	  "environments": {"prod": {"objectives": {"availability_enforcement": "acknowledge"}}, "dev": {}},
	  "db_servers": {
	    "own":     {"connection_string": "host=a", "environment": "prod", "objectives": {"availability_enforcement": "refuse"}},
	    "env":     {"connection_string": "host=b", "environment": "prod"},
	    "default": {"connection_string": "host=c", "environment": "dev"}
	  }
	}`)
	for id, want := range map[string]struct{ value, source string }{
		"own": {"refuse", "database"}, "env": {"acknowledge", "environment:prod"}, "default": {"warn", "default"},
	} {
		res, _ := c.ResolveObjectives(id)
		if !res.AvailabilityEnforcement.Set || res.AvailabilityEnforcement.Value != want.value || res.AvailabilityEnforcement.Source != want.source {
			t.Errorf("%s = %+v, want %s from %s", id, res.AvailabilityEnforcement, want.value, want.source)
		}
	}
	// Nothing set anywhere: warn, and not "invalid".
	none, _ := load(t, `{"db_servers": {"a": {"connection_string": "host=a"}}}`).ResolveObjectives("a")
	if none.EffectiveAvailabilityEnforcement() != EnforceWarn || none.AvailabilityEnforcementInvalid {
		t.Errorf("unset = %q (invalid %v), want warn", none.EffectiveAvailabilityEnforcement(), none.AvailabilityEnforcementInvalid)
	}
}

// A typo must never quietly weaken the check: it falls toward acknowledge, not
// warn, and does not fall through to a more lenient level.
func TestAvailabilityEnforcement_ATypoFallsTowardAcknowledge(t *testing.T) {
	for name, tc := range map[string]struct{ cfg, db string }{
		"a wrong value": {`{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"availability_enforcement": "Refuse"}}}}`, "a"},
		"not a string":  {`{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"availability_enforcement": true}}}}`, "a"},
		"a typo that would have fallen through to the default's warn": {`{
		  "defaults": {"objectives": {"availability_enforcement": "warn"}},
		  "db_servers": {"a": {"connection_string": "host=a", "objectives": {"availability_enforcement": "blok"}}}}`, "a"},
	} {
		c := load(t, tc.cfg)
		res, _ := c.ResolveObjectives(tc.db)
		if !res.AvailabilityEnforcementInvalid || res.EffectiveAvailabilityEnforcement() != EnforceAcknowledge {
			t.Errorf("%s: effective = %q, invalid = %v", name, res.EffectiveAvailabilityEnforcement(), res.AvailabilityEnforcementInvalid)
		}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "availability_enforcement") {
			t.Errorf("%s: Validate = %v, want it to name availability_enforcement", name, err)
		}
	}
	// A database in an environment that is not declared cannot tell what that
	// environment would have set, unless it says for itself.
	c := load(t, `{"db_servers": {
	  "lost":  {"connection_string": "host=a", "environment": "prdo"},
	  "known": {"connection_string": "host=b", "environment": "prdo", "objectives": {"availability_enforcement": "warn"}}}}`)
	if res, _ := c.ResolveObjectives("lost"); res.EffectiveAvailabilityEnforcement() != EnforceAcknowledge {
		t.Errorf("an undeclared environment with nothing set = %q, want acknowledge", res.EffectiveAvailabilityEnforcement())
	}
	if res, _ := c.ResolveObjectives("known"); res.EffectiveAvailabilityEnforcement() != EnforceWarn {
		t.Errorf("what a database sets itself is honoured: %q", res.EffectiveAvailabilityEnforcement())
	}
}

// A mistake at any level is reported at that level, by name.
func TestAvailabilityEnforcement_ValidateNamesTheLevel(t *testing.T) {
	for cfg, want := range map[string]string{
		`{"defaults": {"objectives": {"availability_enforcement": "block"}}}`:                `defaults.objectives: availability_enforcement must be one of warn, acknowledge, refuse, got "block"`,
		`{"environments": {"prod": {"objectives": {"availability_enforcement": "Refuse"}}}}`: `environments.prod.objectives: availability_enforcement must be one of`,
		`{"db_servers": {"a": {"objectives": {"availability_enforcement": 3}}}}`:             `db_servers.a.objectives: availability_enforcement: expected a string`,
	} {
		err := load(t, cfg).Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n got  %v\n want it to contain %q", cfg, err, want)
		}
	}
}

func TestAvailabilityEnforcement_RoundTripsThroughJSON(t *testing.T) {
	c := load(t, `{"defaults": {"objectives": {"availability_enforcement": "acknowledge"}}}`)
	b, err := json.Marshal(c.Defaults.Objectives)
	if err != nil || !strings.Contains(string(b), `"availability_enforcement":"acknowledge"`) {
		t.Errorf("%s, %v", b, err)
	}
}

// --- the config-wide check ---

func TestConsistency(t *testing.T) {
	if got := load(t, fleetConfig).Consistency(); len(got) != 0 {
		t.Errorf("the fleet fixture is consistent, got %v", got)
	}
	var nilCfg *Config
	if got := nilCfg.Consistency(); got != nil {
		t.Errorf("nil config: %v", got)
	}
	if got := load(t, `{"db_servers": {"a": {"connection_string": "host=a"}}}`).Consistency(); len(got) != 0 {
		t.Errorf("a config with no objectives: %v", got)
	}

	// 99.95% over the default 30d allows 21m36s, so a 30m rto contradicts it; the
	// ledger database's 10m rto does not. Listed in a stable order.
	c := load(t, `{
	  "environments": {"prod": {"objectives": {"rto": "30m", "availability_slo": 99.95}}},
	  "db_servers": {
	    "z-orders": {"connection_string": "host=z", "environment": "prod"},
	    "a-orders": {"connection_string": "host=a", "environment": "prod"},
	    "ledger":   {"connection_string": "host=l", "environment": "prod", "objectives": {"rto": "10m"}}
	  }
	}`)
	for i := 0; i < 50; i++ {
		got := c.Consistency()
		if len(got) != 2 || !strings.HasPrefix(got[0], "db_servers.a-orders: ") || !strings.HasPrefix(got[1], "db_servers.z-orders: ") {
			t.Fatalf("Consistency() = %v", got)
		}
	}
	got := c.Consistency()
	for _, line := range got {
		if !strings.Contains(line, "21m36s") || !strings.Contains(line, "rto 30m is longer than the whole budget") || !strings.Contains(line, "(default window)") {
			t.Errorf("line = %q", line)
		}
	}
	// The same config with a wider window is consistent.
	fixed := strings.Replace(`"availability_slo": 99.95`, `99.95`, `99.95, "availability_window": "90d"`, 1)
	c2 := load(t, strings.Replace(`{
	  "environments": {"prod": {"objectives": {"rto": "30m", "availability_slo": 99.95}}},
	  "db_servers": {"a": {"connection_string": "host=a", "environment": "prod"}}
	}`, `"availability_slo": 99.95`, fixed, 1))
	if got := c2.Consistency(); len(got) != 0 {
		t.Errorf("with a 90d window: %v", got)
	}
}

// A contradiction is not an unreadable value: Validate stays quiet and the
// objectives still resolve. LogProblems says so, once per database.
func TestConsistency_IsNotAValidateError(t *testing.T) {
	c := load(t, `{"db_servers": {"a": {"connection_string": "host=a", "objectives": {"rto": "30m", "availability_slo": 99.99}}}}`)
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v; a contradiction between readable values is not a config error", err)
	}
	res, _ := c.ResolveObjectives("a")
	if !res.RTO.Set || !res.AvailabilitySLO.Set || len(res.Problems) != 0 {
		t.Errorf("the objectives were disturbed: %+v", res)
	}
	var lines []string
	n := c.LogProblems(func(msg string, args ...any) {
		lines = append(lines, msg+" "+args[1].(string))
	})
	if n != 1 || len(lines) != 1 || !strings.Contains(lines[0], "contradict each other") || !strings.Contains(lines[0], "db_servers.a") {
		t.Errorf("LogProblems = %d, %v", n, lines)
	}
}

func TestLogProblems_CountsProblemsAndContradictionsTogether(t *testing.T) {
	c := load(t, `{
	  "defaults": {"objectives": {"rto": "0s"}},
	  "db_servers": {"a": {"connection_string": "host=a", "objectives": {"rto": "30m", "availability_slo": 99.99}}}
	}`)
	var lines []string
	n := c.LogProblems(func(msg string, args ...any) { lines = append(lines, msg) })
	// One unreadable value (defaults rto), and the contradiction on a.
	if n != 2 || len(lines) != 2 {
		t.Errorf("LogProblems = %d, %v", n, lines)
	}
}

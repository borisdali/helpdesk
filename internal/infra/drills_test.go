package infra

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func reminderOf(t *testing.T, body, db string) (DrillReminder, ResolvedObjectives) {
	t.Helper()
	cfg := load(t, body)
	res, ok := cfg.ResolveObjectives(db)
	if !ok {
		t.Fatalf("%s is not a database in the config", db)
	}
	return res.DrillReminder(), res
}

func TestDrillReminder_DefaultsToAQuarterForADatabaseWithAnObjective(t *testing.T) {
	r, _ := reminderOf(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"rto":"30m"}}}}`, "a")
	if !r.Applies || r.Interval != DefaultDrillInterval || r.Source != SourceBuiltin || r.Off {
		t.Errorf("%+v", r)
	}
	if DefaultDrillInterval != 90*24*time.Hour {
		t.Errorf("the default is a quarter, got %s", DefaultDrillInterval)
	}
	// Any one objective is enough: rpo, rto, or an availability SLO.
	for _, obj := range []string{`"rpo":"15m"`, `"rto":"30m"`, `"availability_slo":99.9`} {
		r, _ := reminderOf(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{`+obj+`}}}}`, "a")
		if !r.Applies {
			t.Errorf("%s: a database with this objective is not reminded", obj)
		}
	}
}

// A database that says nothing about recovery does not nag: a development
// database is not meant to be drilled every quarter.
func TestDrillReminder_NotForADatabaseWithNoObjective(t *testing.T) {
	r, _ := reminderOf(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a"}}}`, "a")
	if r.Applies || r.Off {
		t.Errorf("%+v", r)
	}
	// Only a drill interval is not an objective: it says how often, not whether it matters.
	r, _ = reminderOf(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"drill_interval":"30d"}}}}`, "a")
	if r.Applies {
		t.Errorf("a drill interval alone made the reminder apply: %+v", r)
	}
	// Other settings that are not objectives do not count either.
	r, _ = reminderOf(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"enforcement":"refuse"}}}}`, "a")
	if r.Applies {
		t.Errorf("enforcement alone made the reminder apply: %+v", r)
	}
}

func TestDrillReminder_ResolvesLikeTheOtherObjectives(t *testing.T) {
	cfg := `{
	  "defaults": {"objectives": {"drill_interval": "120d"}},
	  "environments": {"prod": {"objectives": {"rto": "30m", "drill_interval": "30d"}}, "staging": {"objectives": {"rto": "1h"}}},
	  "db_servers": {
	    "own":     {"name":"own","connection_string":"host=1","environment":"prod","objectives":{"drill_interval":"14d"}},
	    "env":     {"name":"env","connection_string":"host=2","environment":"prod"},
	    "default": {"name":"default","connection_string":"host=3","environment":"staging"}
	  }}`
	for db, want := range map[string]struct {
		d      time.Duration
		source string
	}{
		"own":     {14 * 24 * time.Hour, SourceDatabase},
		"env":     {30 * 24 * time.Hour, "environment:prod"},
		"default": {120 * 24 * time.Hour, SourceDefault},
	} {
		r, _ := reminderOf(t, cfg, db)
		if !r.Applies || r.Interval != want.d || r.Source != want.source {
			t.Errorf("%s: %+v, want %s from %s", db, r, want.d, want.source)
		}
	}
}

func TestDrillReminder_ZeroTurnsItOffOnPurpose(t *testing.T) {
	for _, v := range []string{`"0d"`, `"0s"`} {
		r, _ := reminderOf(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"rto":"30m","drill_interval":`+v+`}}}}`, "a")
		if r.Applies || !r.Off || r.Source != SourceDatabase {
			t.Errorf("%s: %+v", v, r)
		}
	}
	// Off at the fleet level, back on for one database.
	cfg := `{"defaults":{"objectives":{"drill_interval":"0d"}},"db_servers":{
	  "a":{"name":"a","connection_string":"host=a","objectives":{"rto":"30m"}},
	  "b":{"name":"b","connection_string":"host=b","objectives":{"rto":"30m","drill_interval":"60d"}}}}`
	if r, _ := reminderOf(t, cfg, "a"); !r.Off {
		t.Errorf("a: %+v", r)
	}
	if r, _ := reminderOf(t, cfg, "b"); !r.Applies || r.Interval != 60*24*time.Hour {
		t.Errorf("b: %+v", r)
	}
}

// A typo must neither switch the reminder off nor go unnoticed.
func TestDrillReminder_AnUnreadableIntervalFallsBackToTheDefaultAndSaysSo(t *testing.T) {
	for _, bad := range []string{`"soon"`, `"-5d"`, `90`, `"ninety"`} {
		body := `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"rto":"30m","drill_interval":` + bad + `}}}}`
		cfg := load(t, body)
		res, _ := cfg.ResolveObjectives("a")
		r := res.DrillReminder()
		if !r.Applies || r.Interval != DefaultDrillInterval || r.Source != SourceBuiltin {
			t.Errorf("%s: %+v", bad, r)
		}
		if !res.DrillIntervalInvalid || len(res.Problems) == 0 || !strings.Contains(strings.Join(res.Problems, " "), "db_servers.a.objectives") {
			t.Errorf("%s: the problem was not reported: %+v", bad, res.Problems)
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate did not list it", bad)
		}
	}
	// Negative has its own explanation, pointing at how to turn it off.
	cfg := load(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"drill_interval":"-1h"}}}}`)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "drill_interval") {
		t.Errorf("negative: %v", err)
	}
}

// A config that some tool re-serialises reads back with the same reminders: "0d"
// stays off (it is written as "0s", which is the same thing) and an interval does
// not vanish and quietly become the default.
func TestDrillInterval_SurvivesBeingWrittenBackOut(t *testing.T) {
	cfg := load(t, `{
	  "defaults": {"objectives": {"drill_interval": "120d"}},
	  "environments": {"prod": {"objectives": {"rto": "30m", "drill_interval": "30d"}}, "quiet": {"objectives": {"rto": "1h", "drill_interval": "0d"}}},
	  "db_servers": {
	    "env":   {"name":"env","connection_string":"host=1","environment":"prod"},
	    "own":   {"name":"own","connection_string":"host=2","environment":"prod","objectives":{"drill_interval":"14d"}},
	    "off":   {"name":"off","connection_string":"host=3","environment":"quiet"},
	    "back":  {"name":"back","connection_string":"host=4","environment":"quiet","objectives":{"drill_interval":"45d"}},
	    "bare":  {"name":"bare","connection_string":"host=5"}
	  }}`)
	raw, err := json.Marshal(cfg)
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
	for id := range cfg.DBServers {
		a, _ := cfg.ResolveObjectives(id)
		b, _ := back.ResolveObjectives(id)
		if a.DrillReminder() != b.DrillReminder() {
			t.Errorf("%s: %+v became %+v", id, a.DrillReminder(), b.DrillReminder())
		}
	}
	for id, want := range map[string]DrillReminder{
		"env":  {Applies: true, Interval: 30 * 24 * time.Hour, Source: "environment:prod"},
		"own":  {Applies: true, Interval: 14 * 24 * time.Hour, Source: SourceDatabase},
		"off":  {Off: true, Source: "environment:quiet"},
		"back": {Applies: true, Interval: 45 * 24 * time.Hour, Source: SourceDatabase},
		"bare": {},
	} {
		res, _ := back.ResolveObjectives(id)
		if got := res.DrillReminder(); got != want {
			t.Errorf("after the round trip %s: %+v, want %+v", id, got, want)
		}
	}
}

// A mistake in drill_interval costs only that setting: the file loads, every other
// database and objective resolves, and the reminder reports the problem.
func TestDrillInterval_ATypoInTheFileCostsNothingElse(t *testing.T) {
	cfg := load(t, `{"db_servers": {
	  "typo":  {"name":"typo","connection_string":"host=1","objectives":{"rto":"30m","rpo":"15m","drill_interval":"ninety days"}},
	  "fine":  {"name":"fine","connection_string":"host=2","objectives":{"rto":"30m","drill_interval":"60d"}}}}`)
	good, _ := cfg.ResolveObjectives("fine")
	if r := good.DrillReminder(); !r.Applies || r.Interval != 60*24*time.Hour {
		t.Errorf("the other database was affected: %+v", r)
	}
	bad, _ := cfg.ResolveObjectives("typo")
	if !bad.RPO.Set || !bad.RTO.Set {
		t.Errorf("the typo spoiled the database's other objectives: %+v", bad)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "db_servers.typo.objectives") {
		t.Errorf("Validate must say where: %v", err)
	}
	// And the invalid value is never written back as something else.
	if raw, err := json.Marshal(cfg); err == nil {
		t.Errorf("an invalid interval was written out: %s", raw)
	}
}

func TestDrillInterval_DoesNotDisturbTheOtherObjectives(t *testing.T) {
	cfg := load(t, `{"db_servers":{"a":{"name":"a","connection_string":"host=a","objectives":{"rpo":"15m","rto":"30m","drill_interval":"soon"}}}}`)
	res, _ := cfg.ResolveObjectives("a")
	if !res.RPO.Set || !res.RTO.Set || res.RPOInvalid {
		t.Errorf("a bad drill_interval spoiled the rest: %+v", res)
	}
}

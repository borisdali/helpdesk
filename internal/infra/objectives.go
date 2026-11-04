package infra

// Recovery objectives (v0.34): what each database is supposed to achieve, so
// a recovery can be measured against it instead of only reported.
//
//	rpo               the most data (as time) that may be lost
//	rto               the longest a recovery may take
//	availability_slo  the share of time the database should be available, in
//	                  percent (99.9). Recorded and resolved here; nothing
//	                  consumes it yet.
//
// An objective is set in three places, most specific first:
//
//	db_servers.<id>.objectives      this database
//	environments.<env>.objectives   every database in that environment
//	defaults.objectives             the fleet
//
// Each objective resolves on its own: a database that sets only its rpo takes
// its rto from its environment, and the environment's from the fleet default.
// Every resolved value says where it came from.
//
// Tags are free-form strings with no meaning to the config. An environment is
// not: a database names the one it belongs to, and the name must be declared
// under environments.
//
// A mistake in any of this must never cost the rest of the inventory. Most
// services treat a config that fails to load as "no infrastructure config" and
// carry on, so Load does not fail on a bad objective: the file loads, the bad
// value is remembered as invalid, and the objective it belongs to resolves as
// unset with the reason (never as the next level's value, which would be a
// silent fallback). Validate lists every problem, and LogProblems logs them for
// a service to call at startup.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that reads from JSON as a string: "15m",
// "1h30m", and with a days unit, "1d" or "1d12h". A bare number is invalid,
// because "900" would be seconds to one reader and minutes to another.
//
// A value that cannot be read does not fail the file: Invalid says why.
type Duration struct {
	time.Duration
	Invalid string
}

var daysRe = regexp.MustCompile(`^(\d+)d(.*)$`)

// ParseDuration reads a Go duration string, plus a leading days unit.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var days time.Duration
	if m := daysRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > 36500 {
			return 0, fmt.Errorf("invalid number of days in %q", s)
		}
		days = time.Duration(n) * 24 * time.Hour
		s = m[2]
		if s == "" {
			return days, nil
		}
		// "1d-5m" would read as 23h55m: a sign after the days is a mistake.
		if s[0] == '-' || s[0] == '+' {
			return 0, fmt.Errorf("invalid duration %q: no sign after the days", m[0])
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 15m, 1h30m, 1d)", s)
	}
	return days + d, nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		d.Invalid = fmt.Sprintf("a duration is a string such as \"15m\" or \"1d\", not %s", strings.TrimSpace(string(b)))
		return nil
	}
	v, err := ParseDuration(s)
	if err != nil {
		d.Invalid = err.Error()
		return nil
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.Duration.String()) }

// Percent is a percentage from JSON. A value that is not a number does not
// fail the file: Invalid says why.
type Percent struct {
	Value   float64
	Invalid string
}

func (p *Percent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &p.Value); err != nil {
		p.Invalid = fmt.Sprintf("a percentage is a number such as 99.9, not %s", strings.TrimSpace(string(b)))
	}
	return nil
}

func (p Percent) MarshalJSON() ([]byte, error) { return json.Marshal(p.Value) }

// Objectives are the recovery objectives set at one level. An unset field is
// nil: it defers to the next level.
type Objectives struct {
	RPO             *Duration `json:"rpo,omitempty"`
	RTO             *Duration `json:"rto,omitempty"`
	AvailabilitySLO *Percent  `json:"availability_slo,omitempty"`
}

// Environment is a named group of databases that share objectives.
type Environment struct {
	Description string     `json:"description,omitempty"`
	Objectives  Objectives `json:"objectives,omitempty"`
}

// Defaults apply to every database that neither it nor its environment says
// otherwise.
type Defaults struct {
	Objectives Objectives `json:"objectives,omitempty"`
}

var environmentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// problems returns what is wrong with one level's objectives, keyed by
// objective ("rpo", "rto", "availability_slo").
func (o Objectives) problems() map[string]string {
	out := map[string]string{}
	if o.RPO != nil {
		switch {
		case o.RPO.Invalid != "":
			out["rpo"] = o.RPO.Invalid
		case o.RPO.Duration < 0:
			out["rpo"] = "rpo must not be negative"
		}
	}
	if o.RTO != nil {
		switch {
		case o.RTO.Invalid != "":
			out["rto"] = o.RTO.Invalid
		case o.RTO.Duration <= 0:
			out["rto"] = "rto must be greater than zero"
		}
	}
	if o.AvailabilitySLO != nil {
		switch {
		case o.AvailabilitySLO.Invalid != "":
			out["availability_slo"] = o.AvailabilitySLO.Invalid
		case o.AvailabilitySLO.Value <= 0 || o.AvailabilitySLO.Value > 100:
			out["availability_slo"] = fmt.Sprintf("availability_slo is a percentage greater than 0 and at most 100, got %v", o.AvailabilitySLO.Value)
		}
	}
	return out
}

// Validate lists every problem with the objective settings, or returns nil. A
// config that sets none of them is always valid: nothing here constrains an
// existing infrastructure.json.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	check := func(where string, o Objectives) {
		p := o.problems()
		for _, k := range sortedKeys(p) {
			errs = append(errs, fmt.Errorf("%s: %s", where, p[k]))
		}
	}
	if c.Defaults != nil {
		check("defaults.objectives", c.Defaults.Objectives)
	}
	for _, name := range sortedKeys(c.Environments) {
		if !environmentNameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("environments: %q is not a valid environment name (lowercase letters, digits, - and _, starting with a letter or digit, at most 32 characters)", name))
		}
		check(fmt.Sprintf("environments.%s.objectives", name), c.Environments[name].Objectives)
	}
	for _, id := range sortedKeys(c.DBServers) {
		db := c.DBServers[id]
		if db.Environment != "" {
			if _, ok := c.Environments[db.Environment]; !ok {
				errs = append(errs, fmt.Errorf("db_servers.%s: environment %q is not declared under environments (declared: %s)", id, db.Environment, declared(c.Environments)))
			}
		}
		if db.Objectives != nil {
			check(fmt.Sprintf("db_servers.%s.objectives", id), *db.Objectives)
		}
	}
	return errors.Join(errs...)
}

func declared(m map[string]Environment) string {
	if len(m) == 0 {
		return "none"
	}
	return strings.Join(sortedKeys(m), ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Where a resolved objective came from.
const (
	SourceDatabase    = "database"
	SourceEnvironment = "environment" // reported as "environment:<name>"
	SourceDefault     = "default"
)

// ResolvedDuration is one duration objective after resolution.
type ResolvedDuration struct {
	Set    bool          `json:"set"`
	Value  time.Duration `json:"value_ns,omitempty"`
	Source string        `json:"source,omitempty"`
}

// Seconds is the value in whole seconds.
func (r ResolvedDuration) Seconds() int64 { return int64(r.Value / time.Second) }

// ResolvedPercent is one percentage objective after resolution.
type ResolvedPercent struct {
	Set    bool    `json:"set"`
	Value  float64 `json:"value,omitempty"`
	Source string  `json:"source,omitempty"`
}

// ResolvedObjectives is what applies to one database.
type ResolvedObjectives struct {
	// Environment is the database's environment, "" when it has none.
	Environment     string           `json:"environment,omitempty"`
	RPO             ResolvedDuration `json:"rpo"`
	RTO             ResolvedDuration `json:"rto"`
	AvailabilitySLO ResolvedPercent  `json:"availability_slo"`
	// Problems says why an objective is unset although the config tried to set
	// it: a value that cannot be read, or an environment that is not declared.
	// A caller that finds an objective unset must show these, not treat it as
	// "no objective".
	Problems []string `json:"problems,omitempty"`
}

// ResolveObjectives returns the objectives that apply to the database with
// this config key: its own, else its environment's, else the fleet default,
// field by field. ok is false when the key is not a configured database.
//
// A value that is invalid blocks its objective: it resolves as unset and says
// why, it does not fall through to the next level. A database whose
// environment is not declared gets only its own objectives, for the same
// reason: the environment's and the fleet default's cannot be told apart from
// a silent fallback.
func (c *Config) ResolveObjectives(dbID string) (ResolvedObjectives, bool) {
	if c == nil {
		return ResolvedObjectives{}, false
	}
	db, ok := c.DBServers[dbID]
	if !ok {
		return ResolvedObjectives{}, false
	}
	type level struct {
		source string
		o      Objectives
		where  string
	}
	res := ResolvedObjectives{Environment: db.Environment}
	var levels []level
	if db.Objectives != nil {
		levels = append(levels, level{SourceDatabase, *db.Objectives, "db_servers." + dbID + ".objectives"})
	}
	if db.Environment != "" {
		env, declared := c.Environments[db.Environment]
		if !declared {
			res.Problems = append(res.Problems, fmt.Sprintf("environment %q is not declared under environments: only the database's own objectives apply", db.Environment))
		} else {
			levels = append(levels, level{SourceEnvironment + ":" + db.Environment, env.Objectives, "environments." + db.Environment + ".objectives"})
		}
	}
	// The fleet default applies unless the environment is unknown.
	if _, envDeclared := c.Environments[db.Environment]; c.Defaults != nil && (db.Environment == "" || envDeclared) {
		levels = append(levels, level{SourceDefault, c.Defaults.Objectives, "defaults.objectives"})
	}

	var doneRPO, doneRTO, doneAvail bool
	for _, l := range levels {
		p := l.o.problems()
		if !doneRPO && l.o.RPO != nil {
			doneRPO = true
			if msg, bad := p["rpo"]; bad {
				res.Problems = append(res.Problems, l.where+": "+msg)
			} else {
				res.RPO = ResolvedDuration{Set: true, Value: l.o.RPO.Duration, Source: l.source}
			}
		}
		if !doneRTO && l.o.RTO != nil {
			doneRTO = true
			if msg, bad := p["rto"]; bad {
				res.Problems = append(res.Problems, l.where+": "+msg)
			} else {
				res.RTO = ResolvedDuration{Set: true, Value: l.o.RTO.Duration, Source: l.source}
			}
		}
		if !doneAvail && l.o.AvailabilitySLO != nil {
			doneAvail = true
			if msg, bad := p["availability_slo"]; bad {
				res.Problems = append(res.Problems, l.where+": "+msg)
			} else {
				res.AvailabilitySLO = ResolvedPercent{Set: true, Value: l.o.AvailabilitySLO.Value, Source: l.source}
			}
		}
	}
	return res, true
}

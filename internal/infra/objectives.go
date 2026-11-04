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
// not: a database names the one it belongs to, the name must be declared under
// environments, and Load refuses a name that is not. A typo ("prdo") is an
// error, not a database that quietly falls back to the fleet default.

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
// "1h30m", and with a days unit, "1d" or "1d12h". A bare number is refused,
// because "900" would be seconds to one reader and minutes to another.
type Duration struct{ time.Duration }

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
		return fmt.Errorf("a duration is a string such as \"15m\" or \"1d\", not %s", strings.TrimSpace(string(b)))
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.Duration.String()) }

// Objectives are the recovery objectives set at one level. An unset field is
// nil: it defers to the next level.
type Objectives struct {
	RPO             *Duration `json:"rpo,omitempty"`
	RTO             *Duration `json:"rto,omitempty"`
	AvailabilitySLO *float64  `json:"availability_slo,omitempty"`
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

// Validate checks the objective settings. A config that sets none of them is
// always valid: nothing here constrains an existing infrastructure.json.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	check := func(where string, o Objectives) {
		if o.RPO != nil && o.RPO.Duration < 0 {
			errs = append(errs, fmt.Errorf("%s: rpo must not be negative", where))
		}
		if o.RTO != nil && o.RTO.Duration <= 0 {
			errs = append(errs, fmt.Errorf("%s: rto must be greater than zero", where))
		}
		if o.AvailabilitySLO != nil && (*o.AvailabilitySLO <= 0 || *o.AvailabilitySLO > 100) {
			errs = append(errs, fmt.Errorf("%s: availability_slo is a percentage greater than 0 and at most 100, got %v", where, *o.AvailabilitySLO))
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
}

// ResolveObjectives returns the objectives that apply to the database with
// this config key: its own, else its environment's, else the fleet default,
// field by field. ok is false when the key is not a configured database.
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
	}
	var levels []level
	if db.Objectives != nil {
		levels = append(levels, level{SourceDatabase, *db.Objectives})
	}
	if env, ok := c.Environments[db.Environment]; ok && db.Environment != "" {
		levels = append(levels, level{SourceEnvironment + ":" + db.Environment, env.Objectives})
	}
	if c.Defaults != nil {
		levels = append(levels, level{SourceDefault, c.Defaults.Objectives})
	}

	res := ResolvedObjectives{Environment: db.Environment}
	for _, l := range levels {
		if !res.RPO.Set && l.o.RPO != nil {
			res.RPO = ResolvedDuration{Set: true, Value: l.o.RPO.Duration, Source: l.source}
		}
		if !res.RTO.Set && l.o.RTO != nil {
			res.RTO = ResolvedDuration{Set: true, Value: l.o.RTO.Duration, Source: l.source}
		}
		if !res.AvailabilitySLO.Set && l.o.AvailabilitySLO != nil {
			res.AvailabilitySLO = ResolvedPercent{Set: true, Value: *l.o.AvailabilitySLO, Source: l.source}
		}
	}
	return res, true
}

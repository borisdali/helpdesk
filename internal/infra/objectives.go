package infra

// Recovery objectives (v0.34): what each database is supposed to achieve, so
// a recovery can be measured against it instead of only reported.
//
//	rpo               the most data (as time) that may be lost
//	rto               the longest a recovery may take
//	availability_slo  the share of time the database should be available, in
//	                  percent (99.9)
//	availability_window  the rolling window the SLO is measured over ("30d");
//	                  30 days when not set. With the SLO it gives the downtime
//	                  budget, and with the rto, how many worst-case recoveries
//	                  the budget absorbs (see Budget). Only declared numbers
//	                  are used: nothing here measures downtime.
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
	"slices"
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

// MarshalJSON refuses an invalid value. Writing it as "0s" would turn a typo
// into an rpo of zero, which reads as "no data loss tolerated".
func (d Duration) MarshalJSON() ([]byte, error) {
	if d.Invalid != "" {
		return nil, fmt.Errorf("cannot write an invalid duration: %s", d.Invalid)
	}
	return json.Marshal(d.String())
}

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

// MarshalJSON refuses an invalid value, for the same reason as Duration's.
func (p Percent) MarshalJSON() ([]byte, error) {
	if p.Invalid != "" {
		return nil, fmt.Errorf("cannot write an invalid percentage: %s", p.Invalid)
	}
	return json.Marshal(p.Value)
}

// Recovery preference: what to favour when a recovery has to choose between
// getting the database back sooner and losing less data. It decides how a
// recommendation is ranked and how an option that breaches an objective is
// treated; it never decides to act.
const (
	PreferenceAvailabilityFirst = "availability_first"
	PreferenceDataFirst         = "data_first"
)

// Enforcement: what happens when a recovery would breach an objective.
//
//	warn         say so (the default; nothing is blocked)
//	acknowledge  the person approving must acknowledge the breach, separately
//	refuse       the recovery is refused
const (
	EnforceWarn        = "warn"
	EnforceAcknowledge = "acknowledge"
	EnforceRefuse      = "refuse"
)

var (
	validPreferences  = []string{PreferenceAvailabilityFirst, PreferenceDataFirst}
	validEnforcements = []string{EnforceWarn, EnforceAcknowledge, EnforceRefuse}
)

// Choice is a string setting restricted to a fixed set of values. A value that
// is not a string does not fail the file: Invalid says why. Whether the string
// is one of the allowed values is checked by Validate and ResolveObjectives.
type Choice struct {
	Value   string
	Invalid string
}

func (c *Choice) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &c.Value); err != nil {
		c.Invalid = fmt.Sprintf("expected a string, not %s", strings.TrimSpace(string(b)))
	}
	return nil
}

// MarshalJSON refuses an invalid value, for the same reason as Duration's.
func (c Choice) MarshalJSON() ([]byte, error) {
	if c.Invalid != "" {
		return nil, fmt.Errorf("cannot write an invalid setting: %s", c.Invalid)
	}
	return json.Marshal(c.Value)
}

// Objectives are the recovery objectives and recovery policy set at one level.
// An unset field is nil: it defers to the next level.
type Objectives struct {
	RPO             *Duration `json:"rpo,omitempty"`
	RTO             *Duration `json:"rto,omitempty"`
	AvailabilitySLO *Percent  `json:"availability_slo,omitempty"`
	// AvailabilityWindow is the rolling window the availability_slo is measured
	// over. Unset means DefaultAvailabilityWindow.
	AvailabilityWindow *Duration `json:"availability_window,omitempty"`
	// RecoveryPreference is availability_first or data_first.
	RecoveryPreference *Choice `json:"recovery_preference,omitempty"`
	// Enforcement is warn, acknowledge or refuse. Unset means warn. It governs
	// the rpo only (what a restore discards).
	Enforcement *Choice `json:"enforcement,omitempty"`
	// AvailabilityEnforcement is what happens when a restore is expected to
	// breach the availability budget (the downtime the SLO allows): warn,
	// acknowledge or refuse. Unset means warn. It is its own setting, not
	// Enforcement's, because an existing `enforcement: refuse` means "refuse a
	// restore that loses too much data", and must not start refusing for a
	// reason it never named. See budget.go.
	AvailabilityEnforcement *Choice `json:"availability_enforcement,omitempty"`
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
	if o.AvailabilityWindow != nil {
		switch {
		case o.AvailabilityWindow.Invalid != "":
			out["availability_window"] = o.AvailabilityWindow.Invalid
		case o.AvailabilityWindow.Duration <= 0:
			out["availability_window"] = "availability_window must be greater than zero"
		}
	}
	choice := func(key string, c *Choice, allowed []string) {
		if c == nil {
			return
		}
		switch {
		case c.Invalid != "":
			out[key] = key + ": " + c.Invalid
		case !slices.Contains(allowed, c.Value):
			out[key] = fmt.Sprintf("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), c.Value)
		}
	}
	choice("recovery_preference", o.RecoveryPreference, validPreferences)
	choice("enforcement", o.Enforcement, validEnforcements)
	choice("availability_enforcement", o.AvailabilityEnforcement, validEnforcements)
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

// ResolvedChoice is one choice setting after resolution.
type ResolvedChoice struct {
	Set    bool   `json:"set"`
	Value  string `json:"value,omitempty"`
	Source string `json:"source,omitempty"`
}

// ResolvedObjectives is what applies to one database.
type ResolvedObjectives struct {
	// Environment is the database's environment, "" when it has none.
	Environment     string           `json:"environment,omitempty"`
	RPO             ResolvedDuration `json:"rpo"`
	RTO             ResolvedDuration `json:"rto"`
	AvailabilitySLO ResolvedPercent  `json:"availability_slo"`
	// AvailabilityWindow is as declared: unset when nothing sets it. Budget
	// applies DefaultAvailabilityWindow and says so.
	AvailabilityWindow ResolvedDuration `json:"availability_window"`
	RecoveryPreference ResolvedChoice   `json:"recovery_preference"`
	Enforcement        ResolvedChoice   `json:"enforcement"`
	// AvailabilityEnforcement is the enforcement of the availability budget, as
	// declared. AvailabilityEnforcementInvalid says it cannot be trusted, as
	// EnforcementInvalid does for the rpo's.
	AvailabilityEnforcement        ResolvedChoice `json:"availability_enforcement"`
	AvailabilityEnforcementInvalid bool           `json:"availability_enforcement_invalid,omitempty"`
	// AvailabilitySLOInvalid and AvailabilityWindowInvalid say the setting
	// cannot be trusted (unreadable, or the database names an undeclared
	// environment and sets none of its own). Budget then computes nothing: a
	// typo must not quietly become a default window or no objective.
	AvailabilitySLOInvalid    bool `json:"availability_slo_invalid,omitempty"`
	AvailabilityWindowInvalid bool `json:"availability_window_invalid,omitempty"`
	// EnforcementInvalid says the enforcement cannot be trusted: the config set
	// one and got it wrong, or the database names an environment that is not
	// declared and sets none of its own. EffectiveEnforcement then answers
	// acknowledge, not warn: a typo must not quietly weaken the check.
	EnforcementInvalid bool `json:"enforcement_invalid,omitempty"`
	// RPOInvalid says the rpo cannot be trusted: the config set one and got it
	// wrong, or the database names an environment that is not declared and sets
	// no rpo of its own. It is not set when there is simply no rpo, and not by a
	// problem with some other setting: a caller that needs to know whether an
	// unset rpo is "none" or "unknown" reads this, not Problems.
	RPOInvalid bool `json:"rpo_invalid,omitempty"`
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

	// Each setting resolves on its own: the first level that sets it decides,
	// valid or not. An invalid one is reported and does not fall through.
	settings := []struct {
		key     string
		present func(Objectives) bool
		apply   func(r *ResolvedObjectives, o Objectives, source string)
		invalid func(r *ResolvedObjectives)
	}{
		{"rpo", func(o Objectives) bool { return o.RPO != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.RPO = ResolvedDuration{Set: true, Value: o.RPO.Duration, Source: s}
			},
			func(r *ResolvedObjectives) { r.RPOInvalid = true }},
		{"rto", func(o Objectives) bool { return o.RTO != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.RTO = ResolvedDuration{Set: true, Value: o.RTO.Duration, Source: s}
			}, nil},
		{"availability_slo", func(o Objectives) bool { return o.AvailabilitySLO != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.AvailabilitySLO = ResolvedPercent{Set: true, Value: o.AvailabilitySLO.Value, Source: s}
			},
			func(r *ResolvedObjectives) { r.AvailabilitySLOInvalid = true }},
		{"availability_window", func(o Objectives) bool { return o.AvailabilityWindow != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.AvailabilityWindow = ResolvedDuration{Set: true, Value: o.AvailabilityWindow.Duration, Source: s}
			},
			func(r *ResolvedObjectives) { r.AvailabilityWindowInvalid = true }},
		{"recovery_preference", func(o Objectives) bool { return o.RecoveryPreference != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.RecoveryPreference = ResolvedChoice{Set: true, Value: o.RecoveryPreference.Value, Source: s}
			}, nil},
		{"enforcement", func(o Objectives) bool { return o.Enforcement != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.Enforcement = ResolvedChoice{Set: true, Value: o.Enforcement.Value, Source: s}
			},
			func(r *ResolvedObjectives) { r.EnforcementInvalid = true }},
		{"availability_enforcement", func(o Objectives) bool { return o.AvailabilityEnforcement != nil },
			func(r *ResolvedObjectives, o Objectives, s string) {
				r.AvailabilityEnforcement = ResolvedChoice{Set: true, Value: o.AvailabilityEnforcement.Value, Source: s}
			},
			func(r *ResolvedObjectives) { r.AvailabilityEnforcementInvalid = true }},
	}
	for _, s := range settings {
		for _, l := range levels {
			if !s.present(l.o) {
				continue
			}
			if msg, bad := l.o.problems()[s.key]; bad {
				res.Problems = append(res.Problems, l.where+": "+msg)
				if s.invalid != nil {
					s.invalid(&res)
				}
			} else {
				s.apply(&res, l.o, l.source)
			}
			break
		}
	}
	// A database in an environment that is not declared cannot tell which
	// enforcement its environment would have set: it may well be a strict one.
	// Unset would mean warn, so a typo in the environment name would quietly
	// weaken the check exactly as a typo in the enforcement itself would.
	if _, known := c.Environments[db.Environment]; db.Environment != "" && !known {
		if !res.Enforcement.Set {
			res.EnforcementInvalid = true
		}
		// The same goes for the rpo: with no rpo of its own, this database cannot
		// tell what its environment would have required of it.
		if !res.RPO.Set {
			res.RPOInvalid = true
		}
		// And for the availability budget: an slo or a window the environment
		// might have set is unknown, not unset, and must not become the default.
		if !res.AvailabilitySLO.Set {
			res.AvailabilitySLOInvalid = true
		}
		if !res.AvailabilityWindow.Set {
			res.AvailabilityWindowInvalid = true
		}
		// The environment may have set a strict availability enforcement too.
		if !res.AvailabilityEnforcement.Set {
			res.AvailabilityEnforcementInvalid = true
		}
	}
	return res, true
}

// EffectiveAvailabilityEnforcement is what to do when a restore is expected to
// breach the availability budget: the configured one, warn when none is set, and
// acknowledge when it cannot be trusted (an invalid value, or a database in an
// environment that is not declared), as EffectiveEnforcement does for the rpo.
func (r ResolvedObjectives) EffectiveAvailabilityEnforcement() string {
	switch {
	case r.AvailabilityEnforcementInvalid:
		return EnforceAcknowledge
	case r.AvailabilityEnforcement.Set:
		return r.AvailabilityEnforcement.Value
	}
	return EnforceWarn
}

// EffectiveEnforcement is what to do when a recovery would breach an
// objective: the configured enforcement, warn when none is set, and
// acknowledge when it cannot be trusted: the configured value is invalid, or
// the database names an environment that is not declared (a typo must not
// quietly weaken the check).
func (r ResolvedObjectives) EffectiveEnforcement() string {
	switch {
	case r.EnforcementInvalid:
		return EnforceAcknowledge
	case r.Enforcement.Set:
		return r.Enforcement.Value
	}
	return EnforceWarn
}

// Package evidence provides a declarative layer over the objective_evidence
// mechanism (see internal/audit.ObjectiveEvidence): a tool's Go code
// registers a small, fixed set of named, type-safe "probes" that read a
// field off its own typed result; operators author which probes to
// threshold, what to call the resulting signal, and how to describe it, in a
// YAML rules file — without writing or redeploying any Go code.
//
// This is deliberately not a generic reflection/JSONPath system: a rule
// names a probe ("oom_killed"), never a field path ("Pods[].LastState.OOMKilled").
// A typo in a rule's probe name is a plain map lookup miss, caught loudly at
// LoadRules time (fails to start), not a field path that silently resolves
// to nothing forever. The tradeoff: exposing a new field to threshold still
// needs a small Go change (register a probe); tuning thresholds, renaming
// signals, and enabling/disabling existing probes is pure YAML.
package evidence

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"helpdesk/internal/audit"
)

// Kind is the value type a probe extracts.
type Kind int

const (
	KindNumeric Kind = iota
	KindBool
	KindString
)

func (k Kind) String() string {
	switch k {
	case KindNumeric:
		return "numeric"
	case KindBool:
		return "bool"
	case KindString:
		return "string"
	default:
		return "unknown"
	}
}

// erasedProbe is the type-erased form of a typed probe function, stored in
// the package-level registry so LoadRules/Evaluate can look one up by name
// without knowing the concrete item type T.
type erasedProbe struct {
	kind Kind
	fn   func(item any) any // returns float64, bool, or string matching kind
}

// registry maps tool name -> probe name -> probe, populated by ToolSchema.Register.
var registry = map[string]map[string]erasedProbe{}

// ToolSchema declares the probes one tool's typed result exposes, plus how
// to name the resource a given item is about (e.g. a pod or event name).
type ToolSchema[T any] struct {
	Tool     string
	Resource func(T) string
	probes   map[string]erasedProbe
}

// NewToolSchema creates a schema for tool, whose items of type T are
// described (for audit Resource field purposes) by resource.
func NewToolSchema[T any](tool string, resource func(T) string) *ToolSchema[T] {
	return &ToolSchema[T]{Tool: tool, Resource: resource, probes: map[string]erasedProbe{}}
}

// Numeric registers a named probe that extracts a float64 from an item.
func (s *ToolSchema[T]) Numeric(name string, f func(T) float64) *ToolSchema[T] {
	s.probes[name] = erasedProbe{kind: KindNumeric, fn: func(item any) any { return f(item.(T)) }}
	return s
}

// Bool registers a named probe that extracts a bool from an item.
func (s *ToolSchema[T]) Bool(name string, f func(T) bool) *ToolSchema[T] {
	s.probes[name] = erasedProbe{kind: KindBool, fn: func(item any) any { return f(item.(T)) }}
	return s
}

// String registers a named probe that extracts a string from an item.
func (s *ToolSchema[T]) String(name string, f func(T) string) *ToolSchema[T] {
	s.probes[name] = erasedProbe{kind: KindString, fn: func(item any) any { return f(item.(T)) }}
	return s
}

// Register publishes the schema into the package-level registry, keyed by
// s.Tool. Call once per tool, typically from the agent's init() or main().
// Panics on a duplicate tool registration — a programming error, not a
// runtime/config condition.
func (s *ToolSchema[T]) Register() *ToolSchema[T] {
	if _, exists := registry[s.Tool]; exists {
		panic(fmt.Sprintf("evidence: tool %q already registered", s.Tool))
	}
	registry[s.Tool] = s.probes
	return s
}

// Rule is one YAML-authored threshold: if Probe's extracted value compares
// to Threshold via Operator, Signal fires. Rules for a tool are evaluated in
// order, first match per item wins (mirrors the priority a human author
// would expect reading the file top to bottom, and matches how the two
// hand-written functions this package replaces behaved).
type Rule struct {
	// Tool is which registered ToolSchema this rule applies to — a single
	// rules file covers every tool an agent exposes (e.g. both get_pods and
	// get_events for the k8s agent), grouped by this field, rather than one
	// file per tool. Keeps the whole agent's forced-gate conditions in one
	// place a user can open and read top to bottom.
	Tool      string `yaml:"tool"`
	Probe     string `yaml:"probe"`
	Operator  string `yaml:"operator"` // one of: > >= < <= == !=
	Threshold any    `yaml:"threshold"`
	Signal    string `yaml:"signal"`
	// Detail is an optional fmt string applied as fmt.Sprintf(Detail,
	// resource, value) — resource from the schema's Resource func, value
	// from the probe. Empty means no Detail is recorded. Must consume
	// exactly two verbs (resource, then value) — fmt.Sprintf is called with
	// exactly those two arguments regardless of how many verbs Detail has.
	Detail string `yaml:"detail,omitempty"`
}

// LoadRules parses a YAML rules file — a flat list of Rule, each naming its
// own Tool — and validates every rule's Probe name exists in that tool's
// registered schema, and that Operator/Threshold are the right shape for
// that probe's Kind. A rule referencing an unknown probe, tool, or an
// operator/threshold mismatched to the probe's kind is a startup error, not
// a rule that silently never fires. Returns rules grouped by Tool. Every
// Tool named in the file must already be Register()-ed (typically in an
// agent's package-level var init) before LoadRules is called.
func LoadRules(path string) (map[string][]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("evidence: reading rules file %q: %w", path, err)
	}
	var rules []Rule
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("evidence: parsing rules file %q: %w", path, err)
	}
	byTool := map[string][]Rule{}
	for i, r := range rules {
		if r.Tool == "" {
			return nil, fmt.Errorf("evidence: rule %d in %q: missing tool", i, path)
		}
		probes, ok := registry[r.Tool]
		if !ok {
			return nil, fmt.Errorf("evidence: rule %d in %q: tool %q has no registered schema — register it before loading rules", i, path, r.Tool)
		}
		if r.Probe == "" {
			return nil, fmt.Errorf("evidence: rule %d in %q: missing probe", i, path)
		}
		probe, ok := probes[r.Probe]
		if !ok {
			return nil, fmt.Errorf("evidence: rule %d in %q: unknown probe %q for tool %q", i, path, r.Probe, r.Tool)
		}
		if r.Signal == "" {
			return nil, fmt.Errorf("evidence: rule %d in %q: missing signal", i, path)
		}
		if !operatorValidForKind(r.Operator, probe.kind) {
			return nil, fmt.Errorf("evidence: rule %d in %q: operator %q is not valid for probe %q (kind %s)", i, path, r.Operator, r.Probe, probe.kind)
		}
		// Confirm the threshold value itself is the right shape for this
		// probe's kind now, at load time — not the first time this rule
		// happens to evaluate against a real item, which could be much
		// later (or never, for a rarely-hit condition like this one).
		if _, err := compare(probe.kind, zeroValue(probe.kind), r.Operator, r.Threshold); err != nil {
			return nil, fmt.Errorf("evidence: rule %d in %q: threshold %v is not valid for probe %q (kind %s): %w", i, path, r.Threshold, r.Probe, probe.kind, err)
		}
		byTool[r.Tool] = append(byTool[r.Tool], r)
	}
	return byTool, nil
}

// zeroValue returns a representative value of kind, used only to
// type-check a rule's Threshold against compare() at load time.
func zeroValue(kind Kind) any {
	switch kind {
	case KindNumeric:
		return float64(0)
	case KindBool:
		return false
	case KindString:
		return ""
	default:
		return nil
	}
}

// operatorValidForKind reports whether operator is syntactically one of the
// six comparison operators AND semantically applicable to kind (bool/string
// probes only support equality, matching compare()'s own switch).
func operatorValidForKind(op string, kind Kind) bool {
	switch op {
	case ">", ">=", "<", "<=":
		return kind == KindNumeric
	case "==", "!=":
		return true
	default:
		return false
	}
}

// auditor is the minimal interface Evaluate needs from *audit.ToolAuditor —
// kept narrow so tests can pass a fake without constructing a real one.
type auditor interface {
	RecordObjectiveEvidence(ctx context.Context, ev audit.ObjectiveEvidence)
}

// Evaluate runs rules against items (the typed result of a tool call),
// recording objective evidence for the first matching rule per item, in
// rule order. schema.Tool must match the tool the rules were loaded for.
func Evaluate[T any](ctx context.Context, a auditor, schema *ToolSchema[T], items []T, rules []Rule) {
	if a == nil {
		return
	}
	for _, item := range items {
		for _, r := range rules {
			probe, ok := schema.probes[r.Probe]
			if !ok {
				continue // validated at LoadRules time; defensive only
			}
			val := probe.fn(item)
			matched, err := compare(probe.kind, val, r.Operator, r.Threshold)
			if err != nil || !matched {
				continue
			}
			resource := ""
			if schema.Resource != nil {
				resource = schema.Resource(item)
			}
			detail := r.Detail
			if detail != "" {
				detail = fmt.Sprintf(detail, resource, val)
			}
			a.RecordObjectiveEvidence(ctx, audit.ObjectiveEvidence{
				Tool:     schema.Tool,
				Resource: resource,
				Signal:   r.Signal,
				Detail:   detail,
			})
			break // first matching rule per item wins
		}
	}
}

// compare applies operator to (val, threshold), coercing threshold (as
// decoded from YAML — typically float64/int/bool/string) to match kind.
func compare(kind Kind, val any, operator string, threshold any) (bool, error) {
	switch kind {
	case KindNumeric:
		v, ok := val.(float64)
		if !ok {
			return false, fmt.Errorf("evidence: probe value %v is not numeric", val)
		}
		t, ok := toFloat64(threshold)
		if !ok {
			return false, fmt.Errorf("evidence: threshold %v is not numeric", threshold)
		}
		switch operator {
		case ">":
			return v > t, nil
		case ">=":
			return v >= t, nil
		case "<":
			return v < t, nil
		case "<=":
			return v <= t, nil
		case "==":
			return v == t, nil
		case "!=":
			return v != t, nil
		}
	case KindBool:
		v, ok := val.(bool)
		if !ok {
			return false, fmt.Errorf("evidence: probe value %v is not bool", val)
		}
		t, ok := threshold.(bool)
		if !ok {
			return false, fmt.Errorf("evidence: threshold %v is not bool", threshold)
		}
		switch operator {
		case "==":
			return v == t, nil
		case "!=":
			return v != t, nil
		default:
			return false, fmt.Errorf("evidence: operator %q not valid for bool probes", operator)
		}
	case KindString:
		v, ok := val.(string)
		if !ok {
			return false, fmt.Errorf("evidence: probe value %v is not string", val)
		}
		t, ok := threshold.(string)
		if !ok {
			return false, fmt.Errorf("evidence: threshold %v is not string", threshold)
		}
		switch operator {
		case "==":
			return v == t, nil
		case "!=":
			return v != t, nil
		default:
			return false, fmt.Errorf("evidence: operator %q not valid for string probes", operator)
		}
	}
	return false, fmt.Errorf("evidence: unhandled kind %v", kind)
}

// toFloat64 coerces the numeric types yaml.v3 can decode a scalar into.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

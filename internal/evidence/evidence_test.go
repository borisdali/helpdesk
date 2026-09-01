package evidence

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"helpdesk/internal/audit"
)

// fakeAuditor records every ObjectiveEvidence it's given, for assertions.
type fakeAuditor struct {
	recorded []audit.ObjectiveEvidence
}

func (f *fakeAuditor) RecordObjectiveEvidence(_ context.Context, ev audit.ObjectiveEvidence) {
	f.recorded = append(f.recorded, ev)
}

type testItem struct {
	Name     string
	Restarts int
	OOM      bool
	Reason   string
}

// resetRegistry clears package state between tests — Register panics on a
// duplicate tool name, and tests in this file intentionally reuse tool names
// like "test_tool" across subtests.
func resetRegistry(t *testing.T) {
	t.Helper()
	old := registry
	registry = map[string]map[string]erasedProbe{}
	t.Cleanup(func() { registry = old })
}

func testSchema() *ToolSchema[testItem] {
	return NewToolSchema[testItem]("test_tool", func(i testItem) string { return i.Name }).
		Numeric("restart_count", func(i testItem) float64 { return float64(i.Restarts) }).
		Bool("oom_killed", func(i testItem) bool { return i.OOM }).
		String("reason", func(i testItem) string { return i.Reason })
}

func TestRegister_DuplicateTool_Panics(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate tool registration")
		}
	}()
	testSchema().Register()
}

func writeRulesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRules_UnknownTool(t *testing.T) {
	resetRegistry(t)
	path := writeRulesFile(t, `- tool: no_such_tool
  probe: x
  operator: "=="
  threshold: true
  signal: s
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error for unregistered tool")
	}
}

func TestLoadRules_MissingTool(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- probe: restart_count
  operator: ">"
  threshold: 0
  signal: pod_restarted
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error for missing tool field")
	}
}

func TestLoadRules_UnknownProbe(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- tool: test_tool
  probe: restat_count
  operator: ">"
  threshold: 0
  signal: pod_restarted
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error for unknown probe name (typo)")
	}
}

func TestLoadRules_MissingSignal(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- tool: test_tool
  probe: restart_count
  operator: ">"
  threshold: 0
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error for missing signal")
	}
}

func TestLoadRules_OperatorInvalidForBoolProbe(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- tool: test_tool
  probe: oom_killed
  operator: ">"
  threshold: true
  signal: oom_killed
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error: '>' is not valid for a bool probe")
	}
}

func TestLoadRules_OperatorInvalidForStringProbe(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- tool: test_tool
  probe: reason
  operator: "<="
  threshold: "Evicted"
  signal: evicted
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error: '<=' is not valid for a string probe")
	}
}

func TestLoadRules_ThresholdWrongTypeForKind(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- tool: test_tool
  probe: restart_count
  operator: ">"
  threshold: "zero"
  signal: pod_restarted
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error: threshold 'zero' is not numeric")
	}
}

func TestLoadRules_InvalidOperatorSymbol(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `- tool: test_tool
  probe: restart_count
  operator: "~="
  threshold: 0
  signal: pod_restarted
`)
	_, err := LoadRules(path)
	if err == nil {
		t.Fatal("expected error for nonsense operator symbol")
	}
}

func TestLoadRules_ValidFile_GroupsByTool(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	NewToolSchema[testItem]("other_tool", func(i testItem) string { return i.Name }).
		String("reason", func(i testItem) string { return i.Reason }).
		Register()

	path := writeRulesFile(t, `- tool: test_tool
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  detail: "pod %s was OOMKilled"
- tool: test_tool
  probe: restart_count
  operator: ">"
  threshold: 0
  signal: pod_restarted
- tool: other_tool
  probe: reason
  operator: "=="
  threshold: "Evicted"
  signal: evicted
`)
	byTool, err := LoadRules(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(byTool["test_tool"]) != 2 {
		t.Fatalf("got %d test_tool rules, want 2", len(byTool["test_tool"]))
	}
	if len(byTool["other_tool"]) != 1 {
		t.Fatalf("got %d other_tool rules, want 1", len(byTool["other_tool"]))
	}
}

func TestEvaluate_FirstMatchWinsPerItem(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{Tool: "test_tool", Probe: "oom_killed", Operator: "==", Threshold: true, Signal: "oom_killed"},
		{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted"},
	}
	items := []testItem{{Name: "pod-a", Restarts: 3, OOM: true}}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules)

	if len(fa.recorded) != 1 {
		t.Fatalf("got %d recorded events, want 1 (first match only)", len(fa.recorded))
	}
	if fa.recorded[0].Signal != "oom_killed" {
		t.Errorf("got signal %q, want oom_killed (should win over pod_restarted by rule order)", fa.recorded[0].Signal)
	}
}

func TestEvaluate_SecondRuleFiresWhenFirstDoesNotMatch(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{Tool: "test_tool", Probe: "oom_killed", Operator: "==", Threshold: true, Signal: "oom_killed"},
		{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted"},
	}
	items := []testItem{{Name: "pod-b", Restarts: 2, OOM: false}}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules)

	if len(fa.recorded) != 1 || fa.recorded[0].Signal != "pod_restarted" {
		t.Fatalf("got %+v, want exactly one pod_restarted event", fa.recorded)
	}
}

func TestEvaluate_NoRuleMatches_NoEvidenceRecorded(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted"},
	}
	items := []testItem{{Name: "pod-c", Restarts: 0, OOM: false}}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules)

	if len(fa.recorded) != 0 {
		t.Fatalf("got %d recorded events, want 0", len(fa.recorded))
	}
}

func TestEvaluate_DetailFormatting(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted", Detail: "pod %s restarted %v time(s)"},
	}
	items := []testItem{{Name: "pod-d", Restarts: 5}}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules)

	want := "pod pod-d restarted 5 time(s)"
	if len(fa.recorded) != 1 || fa.recorded[0].Detail != want {
		t.Fatalf("got detail %q, want %q", fa.recorded[0].Detail, want)
	}
}

func TestEvaluate_StringProbe_EqualityMatch(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{Tool: "test_tool", Probe: "reason", Operator: "==", Threshold: "Evicted", Signal: "evicted"},
		{Tool: "test_tool", Probe: "reason", Operator: "==", Threshold: "FailedScheduling", Signal: "failed_scheduling"},
	}
	items := []testItem{
		{Name: "ev-1", Reason: "Evicted"},
		{Name: "ev-2", Reason: "FailedScheduling"},
		{Name: "ev-3", Reason: "BackOff"}, // routine noise, no rule matches
	}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules)

	if len(fa.recorded) != 2 {
		t.Fatalf("got %d recorded events, want 2 (BackOff should not match)", len(fa.recorded))
	}
	if fa.recorded[0].Signal != "evicted" || fa.recorded[1].Signal != "failed_scheduling" {
		t.Fatalf("got signals %q, %q", fa.recorded[0].Signal, fa.recorded[1].Signal)
	}
}

func TestEvaluate_NilAuditor_NoPanic(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted"}}
	items := []testItem{{Name: "pod-e", Restarts: 1}}
	var fa auditor
	Evaluate(context.Background(), fa, schema, items, rules) // must not panic
}

// TestEvaluate_TypedNilAuditorPointer_NoPanic covers the real, easy-to-miss
// case: a caller passing an unguarded nil *audit.ToolAuditor (or any
// concrete pointer type implementing auditor) directly as the interface
// argument. Go's `a == nil` treats an interface holding a typed nil pointer
// as non-nil, so *this* is the case a naive nil check misses — the whole
// reason isNilAuditor exists instead of a plain `a == nil`. Without it,
// this test would panic inside fakeAuditor.RecordObjectiveEvidence's own
// field access on a nil receiver, exactly mirroring how
// (*audit.ToolAuditor).RecordObjectiveEvidence's `ta.auditor` field access
// would panic on a nil ta.
func TestEvaluate_TypedNilAuditorPointer_NoPanic(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted"}}
	items := []testItem{{Name: "pod-f", Restarts: 1}}
	var fa *fakeAuditor                                      // typed nil, not a literal nil interface
	Evaluate(context.Background(), fa, schema, items, rules) // must not panic
}

func TestCompare_NumericOperators(t *testing.T) {
	tests := []struct {
		name      string
		operator  string
		val       float64
		threshold float64
		want      bool
	}{
		{"gt_true", ">", 5, 3, true},
		{"gt_false", ">", 3, 5, false},
		{"gte_equal_true", ">=", 5, 5, true},
		{"gte_greater_true", ">=", 6, 5, true},
		{"gte_false", ">=", 4, 5, false},
		{"lt_true", "<", 3, 5, true},
		{"lt_false", "<", 5, 3, false},
		{"lte_equal_true", "<=", 5, 5, true},
		{"lte_less_true", "<=", 4, 5, true},
		{"lte_false", "<=", 6, 5, false},
		{"eq_true", "==", 5, 5, true},
		{"eq_false", "==", 5, 6, false},
		{"neq_true", "!=", 5, 6, true},
		{"neq_false", "!=", 5, 5, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compare(KindNumeric, tc.val, tc.operator, tc.threshold)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("compare(%v, %q, %v) = %v, want %v", tc.val, tc.operator, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestCompare_BoolOperators(t *testing.T) {
	tests := []struct {
		name      string
		operator  string
		val       bool
		threshold bool
		want      bool
	}{
		{"eq_true", "==", true, true, true},
		{"eq_false", "==", true, false, false},
		{"neq_true", "!=", true, false, true},
		{"neq_false", "!=", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compare(KindBool, tc.val, tc.operator, tc.threshold)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("compare(%v, %q, %v) = %v, want %v", tc.val, tc.operator, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestCompare_StringOperators(t *testing.T) {
	tests := []struct {
		name      string
		operator  string
		val       string
		threshold string
		want      bool
	}{
		{"eq_true", "==", "Evicted", "Evicted", true},
		{"eq_false", "==", "Evicted", "BackOff", false},
		{"neq_true", "!=", "Evicted", "BackOff", true},
		{"neq_false", "!=", "Evicted", "Evicted", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compare(KindString, tc.val, tc.operator, tc.threshold)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("compare(%q, %q, %q) = %v, want %v", tc.val, tc.operator, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestEvaluate_UnregisteredProbe_SkipsDefensively covers Evaluate's
// defensive "probe not found" branch — unreachable when rules come from
// LoadRules (which validates every probe name exists), but Evaluate is a
// public function and can be called with manually-constructed Rules that
// bypass that validation entirely.
func TestEvaluate_UnregisteredProbe_SkipsDefensively(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{Tool: "test_tool", Probe: "no_such_probe", Operator: "==", Threshold: true, Signal: "should_never_fire"},
		{Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0), Signal: "pod_restarted"},
	}
	items := []testItem{{Name: "pod-f", Restarts: 1}}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules) // must not panic

	if len(fa.recorded) != 1 || fa.recorded[0].Signal != "pod_restarted" {
		t.Fatalf("got %+v, want the unregistered-probe rule skipped and pod_restarted to fire", fa.recorded)
	}
}

func TestCompare_TypeMismatchReturnsError(t *testing.T) {
	if _, err := compare(KindNumeric, "not-a-number", ">", float64(0)); err == nil {
		t.Error("expected error for non-numeric probe value")
	}
	if _, err := compare(KindBool, "not-a-bool", "==", true); err == nil {
		t.Error("expected error for non-bool probe value")
	}
	if _, err := compare(KindString, 5, "==", "x"); err == nil {
		t.Error("expected error for non-string probe value")
	}
}

// ── confirmation fields on Rule/LoadRules/Evaluate ──────────────────────────

func TestLoadRules_NoConfirmationFields_Valid(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `
- tool: test_tool
  probe: restart_count
  operator: ">"
  threshold: 0
  signal: pod_restarted
`)
	if _, err := LoadRules(path); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
}

func TestLoadRules_ConfirmationProbe_Valid(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `
- tool: test_tool
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  confirmation_probe: resource_named_in_quote
`)
	if _, err := LoadRules(path); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
}

func TestLoadRules_ConfirmationProbe_Unknown(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `
- tool: test_tool
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  confirmation_probe: does_not_exist
`)
	if _, err := LoadRules(path); err == nil {
		t.Fatal("expected error for unknown confirmation_probe")
	}
}

func TestLoadRules_ConfirmationProbe_NumericWithoutThreshold(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `
- tool: test_tool
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  confirmation_probe: primary_confidence
`)
	if _, err := LoadRules(path); err == nil {
		t.Fatal("expected error: primary_confidence (numeric) has no universal default threshold")
	}
}

func TestLoadRules_ConfirmationProbe_NumericWithThreshold_Valid(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	path := writeRulesFile(t, `
- tool: test_tool
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  confirmation_probe: primary_confidence
  confirmation_operator: ">="
  confirmation_threshold: 0.6
`)
	if _, err := LoadRules(path); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
}

func TestLoadRules_ConfirmationOperator_InvalidForKind(t *testing.T) {
	resetRegistry(t)
	testSchema().Register()
	// resource_named_in_quote is bool-kind — ">" only applies to numeric probes.
	path := writeRulesFile(t, `
- tool: test_tool
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  confirmation_probe: resource_named_in_quote
  confirmation_operator: ">"
  confirmation_threshold: true
`)
	if _, err := LoadRules(path); err == nil {
		t.Fatal("expected error for '>' operator on a bool-kind confirmation probe")
	}
}

func TestEvaluate_PopulatesValueAndConfirmationFields(t *testing.T) {
	resetRegistry(t)
	schema := testSchema().Register()
	rules := []Rule{
		{
			Tool: "test_tool", Probe: "restart_count", Operator: ">", Threshold: float64(0),
			Signal: "pod_restarted",
			// no confirmation_* fields — Value must still be populated.
		},
		{
			Tool: "test_tool", Probe: "oom_killed", Operator: "==", Threshold: true,
			Signal: "oom_killed", ConfirmationProbe: "resource_named_in_quote",
		},
	}
	items := []testItem{{Name: "pod-g", Restarts: 3, OOM: true}}
	fa := &fakeAuditor{}
	Evaluate(context.Background(), fa, schema, items, rules)

	if len(fa.recorded) != 1 {
		t.Fatalf("got %d recorded events, want 1 (first matching rule per item wins)", len(fa.recorded))
	}
	ev := fa.recorded[0]
	if ev.Signal != "pod_restarted" {
		t.Fatalf("Signal = %q, want pod_restarted", ev.Signal)
	}
	if ev.Value != float64(3) {
		t.Errorf("Value = %v, want 3", ev.Value)
	}
	if ev.ConfirmationProbe != "" {
		t.Errorf("ConfirmationProbe = %q, want empty (rule declared none)", ev.ConfirmationProbe)
	}
}

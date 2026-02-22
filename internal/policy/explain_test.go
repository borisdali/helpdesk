package policy

import (
	"strings"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func assertContains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("expected output to contain %q\nfull output:\n%s", sub, s)
	}
}

func assertNotContains(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("expected output NOT to contain %q\nfull output:\n%s", sub, s)
	}
}

func newEngineFromYAML(t *testing.T, yaml string) *Engine {
	t.Helper()
	cfg, err := Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectDeny})
}

// ── buildExplanation ─────────────────────────────────────────────────────────

func TestBuildExplanation_DefaultDeny_NoTags(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "mystery-db"},
		Action:   ActionWrite,
	}
	trace := DecisionTrace{
		Decision:       Decision{Effect: EffectDeny, PolicyName: "default"},
		DefaultApplied: true,
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "mystery-db")
	assertContains(t, got, "DENIED")
	// Diagnostic hint about missing tags should appear when tags are absent.
	assertContains(t, got, "no tags")
}

func TestBuildExplanation_DefaultDeny_WithTags(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "mystery-db", Tags: []string{"staging"}},
		Action:   ActionWrite,
	}
	trace := DecisionTrace{
		Decision:       Decision{Effect: EffectDeny, PolicyName: "default"},
		DefaultApplied: true,
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "staging")
	assertContains(t, got, "DENIED")
	// Tags are present — the "no tags" diagnostic should NOT appear.
	assertNotContains(t, got, "no tags")
}

func TestBuildExplanation_Allowed(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "dev-db"},
		Action:   ActionRead,
	}
	trace := DecisionTrace{
		Decision: Decision{Effect: EffectAllow, PolicyName: "read-policy"},
		PoliciesEvaluated: []PolicyTrace{
			{
				PolicyName: "read-policy",
				Matched:    true,
				Rules:      []RuleTrace{{Index: 0, Actions: []string{"read"}, Effect: "allow", Matched: true}},
			},
		},
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "ALLOWED")
	assertContains(t, got, "read-policy")
	assertContains(t, got, "permitted")
}

func TestBuildExplanation_DeniedExplicitRule(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db", Tags: []string{"production"}},
		Action:   ActionDestructive,
	}
	trace := DecisionTrace{
		Decision: Decision{
			Effect:     EffectDeny,
			PolicyName: "prod-protection",
			RuleIndex:  2,
			Message:    "Destructive ops prohibited on production",
		},
		PoliciesEvaluated: []PolicyTrace{
			{
				PolicyName: "prod-protection",
				Matched:    true,
				Rules: []RuleTrace{
					{Index: 0, Actions: []string{"read"},        Effect: "allow", Matched: false, SkipReason: "action_mismatch"},
					{Index: 1, Actions: []string{"write"},       Effect: "allow", Matched: false, SkipReason: "action_mismatch"},
					{Index: 2, Actions: []string{"destructive"}, Effect: "deny",  Matched: true},
				},
			},
		},
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "DENIED")
	assertContains(t, got, "prod-protection")
	assertContains(t, got, "action_mismatch")
	assertContains(t, got, "Destructive ops prohibited")
}

func TestBuildExplanation_RequiresApproval(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db", Tags: []string{"production"}},
		Action:   ActionWrite,
	}
	trace := DecisionTrace{
		Decision: Decision{
			Effect:           EffectRequireApproval,
			PolicyName:       "prod-policy",
			RequiresApproval: true,
			ApprovalQuorum:   1,
		},
		PoliciesEvaluated: []PolicyTrace{
			{
				PolicyName: "prod-policy",
				Matched:    true,
				Rules: []RuleTrace{
					{
						Index: 0, Actions: []string{"write"}, Effect: "require_approval", Matched: true,
						Conditions: []ConditionTrace{
							{Name: "require_approval", Passed: true, Detail: "quorum: 1"},
						},
					},
				},
			},
		},
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "REQUIRES APPROVAL")
	assertContains(t, got, "approvals list")
	assertContains(t, got, "✓")
}

func TestBuildExplanation_BlastRadius_ConditionFailed(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionWrite,
		Context:  RequestContext{RowsAffected: 1500},
	}
	trace := DecisionTrace{
		Decision: Decision{
			Effect:     EffectDeny,
			PolicyName: "blast-radius-policy",
			Message:    "Operation affects 1500 rows, limit is 100",
		},
		PoliciesEvaluated: []PolicyTrace{
			{
				PolicyName: "blast-radius-policy",
				Matched:    true,
				Rules: []RuleTrace{
					{
						Index: 0, Actions: []string{"write"}, Effect: "deny", Matched: true,
						Conditions: []ConditionTrace{
							{Name: "max_rows_affected", Passed: false, Detail: "1500 rows affected, limit is 100"},
						},
					},
				},
			},
		},
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "DENIED")
	assertContains(t, got, "✗")
	assertContains(t, got, "1500")
	assertContains(t, got, "100")
}

func TestBuildExplanation_BlastRadius_ConditionPassed(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "dev-db"},
		Action:   ActionWrite,
		Context:  RequestContext{RowsAffected: 5},
	}
	trace := DecisionTrace{
		Decision: Decision{Effect: EffectAllow, PolicyName: "blast-policy"},
		PoliciesEvaluated: []PolicyTrace{
			{
				PolicyName: "blast-policy",
				Matched:    true,
				Rules: []RuleTrace{
					{
						Index: 0, Actions: []string{"write"}, Effect: "allow", Matched: true,
						Conditions: []ConditionTrace{
							{Name: "max_rows_affected", Passed: true, Detail: "5 rows affected, limit is 100"},
						},
					},
				},
			},
		},
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "✓")
	assertNotContains(t, got, "✗")
}

func TestBuildExplanation_PolicySkipped_Various(t *testing.T) {
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db", Tags: []string{"production"}},
		Action:   ActionWrite,
	}
	trace := DecisionTrace{
		Decision: Decision{Effect: EffectAllow, PolicyName: "write-policy"},
		PoliciesEvaluated: []PolicyTrace{
			{PolicyName: "dba-only",    Matched: false, SkipReason: "principal_mismatch"},
			{PolicyName: "turned-off",  Matched: false, SkipReason: "disabled"},
			{PolicyName: "k8s-only",    Matched: false, SkipReason: "resource_mismatch"},
			{
				PolicyName: "write-policy",
				Matched:    true,
				Rules:      []RuleTrace{{Index: 0, Actions: []string{"write"}, Effect: "allow", Matched: true}},
			},
		},
	}
	got := buildExplanation(req, trace)

	assertContains(t, got, "principal_mismatch")
	assertContains(t, got, "disabled")
	assertContains(t, got, "resource_mismatch")
	assertContains(t, got, "ALLOWED")
}

// ── Explain() — trace structure ───────────────────────────────────────────────

func TestExplain_DefaultApplied_WhenNoResourceMatch(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: prod-only
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: read
        effect: allow
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "dev-db", Tags: []string{"development"}},
		Action:   ActionRead,
	}
	trace := engine.Explain(req)

	if !trace.DefaultApplied {
		t.Error("DefaultApplied should be true when no policy matched")
	}
	if trace.Decision.PolicyName != "default" {
		t.Errorf("PolicyName = %q, want default", trace.Decision.PolicyName)
	}
	if len(trace.PoliciesEvaluated) != 1 {
		t.Fatalf("PoliciesEvaluated len = %d, want 1", len(trace.PoliciesEvaluated))
	}
	if trace.PoliciesEvaluated[0].SkipReason != "resource_mismatch" {
		t.Errorf("SkipReason = %q, want resource_mismatch", trace.PoliciesEvaluated[0].SkipReason)
	}
}

func TestExplain_ExplanationFieldPopulated(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: deny-write
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        message: "writes not allowed here"
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "test-db"},
		Action:   ActionWrite,
	}
	trace := engine.Explain(req)

	if trace.Explanation == "" {
		t.Error("Explanation should be non-empty")
	}
	assertContains(t, trace.Explanation, "DENIED")
	assertContains(t, trace.Explanation, "writes not allowed here")
}

func TestExplain_RuleSkipReasons_ActionMismatch(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: multi-rule
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: allow
      - action: destructive
        effect: deny
        message: "no destructive"
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionDestructive,
	}
	trace := engine.Explain(req)

	if len(trace.PoliciesEvaluated) != 1 {
		t.Fatalf("PoliciesEvaluated len = %d, want 1", len(trace.PoliciesEvaluated))
	}
	rules := trace.PoliciesEvaluated[0].Rules
	if len(rules) != 3 {
		t.Fatalf("rules len = %d, want 3", len(rules))
	}

	for _, i := range []int{0, 1} {
		if rules[i].Matched {
			t.Errorf("rule %d should not be matched", i)
		}
		if rules[i].SkipReason != "action_mismatch" {
			t.Errorf("rule %d SkipReason = %q, want action_mismatch", i, rules[i].SkipReason)
		}
	}
	if !rules[2].Matched {
		t.Error("rule 2 should be matched")
	}
	if trace.Decision.Effect != EffectDeny {
		t.Errorf("effect = %q, want deny", trace.Decision.Effect)
	}
}

func TestExplain_RuleSkipReason_ScheduleInactive(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: freeze
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        conditions:
          schedule:
            days: [mon]
            hours: [9]
        message: "freeze active"
      - action: write
        effect: allow
`)
	// Saturday — the Monday/9am schedule is inactive; falls through to allow.
	saturday := time.Date(2026, 2, 21, 9, 0, 0, 0, time.UTC)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
		Context:  RequestContext{Timestamp: saturday},
	}
	trace := engine.Explain(req)

	if trace.Decision.Effect != EffectAllow {
		t.Errorf("Saturday: effect = %q, want allow", trace.Decision.Effect)
	}
	rules := trace.PoliciesEvaluated[0].Rules
	if len(rules) != 2 {
		t.Fatalf("rules len = %d, want 2", len(rules))
	}
	if rules[0].SkipReason != "schedule_inactive" {
		t.Errorf("rule 0 SkipReason = %q, want schedule_inactive", rules[0].SkipReason)
	}
	if !rules[1].Matched {
		t.Error("rule 1 should be matched")
	}
}

func TestExplain_PolicySkipped_Disabled(t *testing.T) {
	disabled := false
	cfg := &Config{
		Policies: []Policy{
			{
				Name:    "disabled-pol",
				Enabled: &disabled,
				Resources: []Resource{{Type: "database"}},
				Rules:     []Rule{{Action: ActionMatcher{ActionWrite}, Effect: EffectAllow}},
			},
		},
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectDeny})
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
	}
	trace := engine.Explain(req)

	if !trace.DefaultApplied {
		t.Error("DefaultApplied should be true — disabled policy should not match")
	}
	if len(trace.PoliciesEvaluated) != 1 {
		t.Fatalf("PoliciesEvaluated len = %d, want 1", len(trace.PoliciesEvaluated))
	}
	if trace.PoliciesEvaluated[0].SkipReason != "disabled" {
		t.Errorf("SkipReason = %q, want disabled", trace.PoliciesEvaluated[0].SkipReason)
	}
}

func TestExplain_PolicySkipped_PrincipalMismatch(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: dba-only
    principals:
      - role: dba
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
`)
	req := Request{
		Principal: RequestPrincipal{UserID: "bob", Roles: []string{"developer"}},
		Resource:  RequestResource{Type: "database", Name: "db"},
		Action:    ActionWrite,
	}
	trace := engine.Explain(req)

	if len(trace.PoliciesEvaluated) != 1 {
		t.Fatalf("PoliciesEvaluated len = %d, want 1", len(trace.PoliciesEvaluated))
	}
	if trace.PoliciesEvaluated[0].SkipReason != "principal_mismatch" {
		t.Errorf("SkipReason = %q, want principal_mismatch", trace.PoliciesEvaluated[0].SkipReason)
	}
}

func TestExplain_ConditionTrace_RequireApproval(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: approval-needed
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          require_approval: true
          approval_quorum: 2
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
	}
	trace := engine.Explain(req)

	if trace.Decision.Effect != EffectRequireApproval {
		t.Errorf("effect = %q, want require_approval", trace.Decision.Effect)
	}
	rules := trace.PoliciesEvaluated[0].Rules
	if len(rules) != 1 || !rules[0].Matched {
		t.Fatal("expected 1 matched rule")
	}
	if len(rules[0].Conditions) != 1 {
		t.Fatalf("conditions len = %d, want 1", len(rules[0].Conditions))
	}
	ct := rules[0].Conditions[0]
	if ct.Name != "require_approval" {
		t.Errorf("condition Name = %q, want require_approval", ct.Name)
	}
	if !ct.Passed {
		t.Error("require_approval condition should be Passed=true")
	}
	if !strings.Contains(ct.Detail, "2") {
		t.Errorf("Detail should mention quorum 2, got %q", ct.Detail)
	}
}

func TestExplain_ConditionTrace_BlastRadius_Pass(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: blast
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          max_rows_affected: 100
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
		Context:  RequestContext{RowsAffected: 50},
	}
	trace := engine.Explain(req)

	if trace.Decision.Effect != EffectAllow {
		t.Errorf("50 rows: effect = %q, want allow", trace.Decision.Effect)
	}
	ct := trace.PoliciesEvaluated[0].Rules[0].Conditions[0]
	if ct.Name != "max_rows_affected" {
		t.Errorf("condition Name = %q, want max_rows_affected", ct.Name)
	}
	if !ct.Passed {
		t.Error("50 rows should pass a limit of 100")
	}
	if !strings.Contains(ct.Detail, "50") || !strings.Contains(ct.Detail, "100") {
		t.Errorf("Detail should mention 50 and 100, got %q", ct.Detail)
	}
}

func TestExplain_ConditionTrace_BlastRadius_Fail(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: blast
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          max_rows_affected: 100
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
		Context:  RequestContext{RowsAffected: 999},
	}
	trace := engine.Explain(req)

	if trace.Decision.Effect != EffectDeny {
		t.Errorf("999 rows: effect = %q, want deny", trace.Decision.Effect)
	}
	ct := trace.PoliciesEvaluated[0].Rules[0].Conditions[0]
	if ct.Passed {
		t.Error("999 rows should fail a limit of 100")
	}
	if !strings.Contains(ct.Detail, "999") || !strings.Contains(ct.Detail, "100") {
		t.Errorf("Detail should mention 999 and 100, got %q", ct.Detail)
	}
}

func TestExplain_BothConditions_RequireApprovalAndBlastRadius(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: combined
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          require_approval: true
          max_rows_affected: 100
`)
	// Both conditions present but rows within limit.
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
		Context:  RequestContext{RowsAffected: 10},
	}
	trace := engine.Explain(req)

	if trace.Decision.Effect != EffectRequireApproval {
		t.Errorf("effect = %q, want require_approval", trace.Decision.Effect)
	}
	conditions := trace.PoliciesEvaluated[0].Rules[0].Conditions
	if len(conditions) != 2 {
		t.Fatalf("conditions len = %d, want 2", len(conditions))
	}
	names := map[string]bool{}
	for _, c := range conditions {
		names[c.Name] = true
	}
	if !names["require_approval"] || !names["max_rows_affected"] {
		t.Errorf("expected both conditions in trace, got names: %v", names)
	}
}

func TestExplain_MultiplePolicies_FirstMatchWins(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: strict
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: write
        effect: deny
  - name: permissive
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db", Tags: []string{"production"}},
		Action:   ActionWrite,
	}
	trace := engine.Explain(req)

	if trace.Decision.PolicyName != "strict" {
		t.Errorf("PolicyName = %q, want strict (first match should win)", trace.Decision.PolicyName)
	}
	if trace.Decision.Effect != EffectDeny {
		t.Errorf("effect = %q, want deny", trace.Decision.Effect)
	}
	// Evaluation stops at the first matched policy — permissive is never visited.
	if len(trace.PoliciesEvaluated) != 1 {
		t.Errorf("PoliciesEvaluated len = %d, want 1 (stops at first match)", len(trace.PoliciesEvaluated))
	}
}

func TestExplain_MultiplePolicies_SecondMatchesWhenFirstResourceMisses(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: prod-only
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: write
        effect: deny
  - name: all-dbs
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
`)
	// dev-db doesn't have the production tag, so prod-only is skipped.
	req := Request{
		Resource: RequestResource{Type: "database", Name: "dev-db", Tags: []string{"development"}},
		Action:   ActionWrite,
	}
	trace := engine.Explain(req)

	if trace.Decision.PolicyName != "all-dbs" {
		t.Errorf("PolicyName = %q, want all-dbs", trace.Decision.PolicyName)
	}
	if trace.Decision.Effect != EffectAllow {
		t.Errorf("effect = %q, want allow", trace.Decision.Effect)
	}
	// First policy was skipped (resource mismatch), second matched.
	if len(trace.PoliciesEvaluated) != 2 {
		t.Fatalf("PoliciesEvaluated len = %d, want 2", len(trace.PoliciesEvaluated))
	}
	if trace.PoliciesEvaluated[0].SkipReason != "resource_mismatch" {
		t.Errorf("first policy SkipReason = %q, want resource_mismatch", trace.PoliciesEvaluated[0].SkipReason)
	}
	if !trace.PoliciesEvaluated[1].Matched {
		t.Error("second policy should be matched")
	}
}

func TestExplain_DryRunFlipsEffectButKeepsTrace(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: deny-all
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        message: "all writes denied"
`)
	engine.dryRun = true
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
	}
	trace := engine.Explain(req)

	// Effect is flipped to allow in dry-run.
	if trace.Decision.Effect != EffectAllow {
		t.Errorf("dry-run: effect = %q, want allow", trace.Decision.Effect)
	}
	// But the trace and explanation are still populated.
	if len(trace.PoliciesEvaluated) == 0 {
		t.Error("PoliciesEvaluated should be non-empty even in dry-run")
	}
	if trace.Explanation == "" {
		t.Error("Explanation should be non-empty even in dry-run")
	}
	// Message gets the [DRY RUN] prefix.
	if !strings.HasPrefix(trace.Decision.Message, "[DRY RUN]") {
		t.Errorf("dry-run message = %q, expected [DRY RUN] prefix", trace.Decision.Message)
	}
}

// ── DeniedError ───────────────────────────────────────────────────────────────

func TestDeniedError_ExplanationOverridesTerseMessage(t *testing.T) {
	err := &DeniedError{
		Decision:    Decision{Effect: EffectDeny, PolicyName: "test", Message: "terse message"},
		Explanation: "long detailed explanation with full trace context",
	}
	if err.Error() != "long detailed explanation with full trace context" {
		t.Errorf("Error() = %q, expected explanation string", err.Error())
	}
}

func TestDeniedError_FallsBackToMessage(t *testing.T) {
	err := &DeniedError{
		Decision: Decision{Effect: EffectDeny, PolicyName: "test-policy", Message: "short denial message"},
	}
	got := err.Error()
	if !strings.Contains(got, "short denial message") {
		t.Errorf("Error() = %q, expected 'short denial message'", got)
	}
}

func TestDeniedError_FallsBackToPolicyName(t *testing.T) {
	err := &DeniedError{
		Decision: Decision{Effect: EffectDeny, PolicyName: "fallback-policy"},
	}
	got := err.Error()
	if !strings.Contains(got, "fallback-policy") {
		t.Errorf("Error() = %q, expected 'fallback-policy'", got)
	}
}

// ── Evaluate() still works (backwards compat) ─────────────────────────────────

func TestEvaluate_StillReturnsDecision(t *testing.T) {
	engine := newEngineFromYAML(t, `
version: "1"
policies:
  - name: allow-reads
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
`)
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionRead,
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("Evaluate: effect = %q, want allow", decision.Effect)
	}
	if decision.PolicyName != "allow-reads" {
		t.Errorf("Evaluate: PolicyName = %q, want allow-reads", decision.PolicyName)
	}
}

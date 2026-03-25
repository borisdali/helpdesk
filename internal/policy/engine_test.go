package policy

import (
	"testing"
	"time"
)

func TestLoadAndEvaluate(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: test-policy
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: read
        effect: allow
      - action: write
        effect: allow
        conditions:
          require_approval: true
      - action: destructive
        effect: deny
        message: "No destructive ops on prod"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	tests := []struct {
		name       string
		action     ActionClass
		tags       []string
		wantEffect Effect
	}{
		{"read-prod", ActionRead, []string{"production"}, EffectAllow},
		{"write-prod", ActionWrite, []string{"production"}, EffectRequireApproval},
		{"destructive-prod", ActionDestructive, []string{"production"}, EffectDeny},
		{"read-dev", ActionRead, []string{"development"}, EffectDeny}, // No matching policy -> default deny
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{
				Resource: RequestResource{
					Type: "database",
					Name: "test-db",
					Tags: tt.tags,
				},
				Action: tt.action,
			}
			decision := engine.Evaluate(req)
			if decision.Effect != tt.wantEffect {
				t.Errorf("got effect %q, want %q", decision.Effect, tt.wantEffect)
			}
		})
	}
}

func TestPrincipalMatching(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: dba-policy
    principals:
      - role: dba
    resources:
      - type: database
    rules:
      - action: destructive
        effect: allow
  - name: default-policy
    resources:
      - type: database
    rules:
      - action: destructive
        effect: deny
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// DBA should be allowed
	dbaReq := Request{
		Principal: RequestPrincipal{
			UserID: "alice",
			Roles:  []string{"dba"},
		},
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionDestructive,
	}
	decision := engine.Evaluate(dbaReq)
	if decision.Effect != EffectAllow {
		t.Errorf("DBA should be allowed, got %q", decision.Effect)
	}

	// Non-DBA should be denied
	userReq := Request{
		Principal: RequestPrincipal{
			UserID: "bob",
			Roles:  []string{"developer"},
		},
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionDestructive,
	}
	decision = engine.Evaluate(userReq)
	if decision.Effect != EffectDeny {
		t.Errorf("Non-DBA should be denied, got %q", decision.Effect)
	}
}

func TestScheduleCondition(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: business-hours-freeze
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        conditions:
          schedule:
            days: [mon, tue, wed, thu, fri]
            hours: [9, 10, 11, 12, 13, 14, 15, 16]
        message: "No changes during business hours"
      - action: write
        effect: allow
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// Monday at 10am should be denied
	mondayMorning := time.Date(2026, 2, 16, 10, 0, 0, 0, time.UTC) // Monday
	req := Request{
		Resource: RequestResource{Type: "database", Name: "test-db"},
		Action:   ActionWrite,
		Context:  RequestContext{Timestamp: mondayMorning},
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectDeny {
		t.Errorf("Monday 10am should be denied, got %q", decision.Effect)
	}

	// Saturday at 10am should be allowed
	saturdayMorning := time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC) // Saturday
	req.Context.Timestamp = saturdayMorning
	decision = engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("Saturday 10am should be allowed, got %q", decision.Effect)
	}

	// Monday at 8pm should be allowed
	mondayEvening := time.Date(2026, 2, 16, 20, 0, 0, 0, time.UTC) // Monday 8pm
	req.Context.Timestamp = mondayEvening
	decision = engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("Monday 8pm should be allowed, got %q", decision.Effect)
	}
}

func TestBlastRadiusLimit(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: blast-radius
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          max_rows_affected: 100
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// 50 rows should be allowed
	req := Request{
		Resource: RequestResource{Type: "database", Name: "test-db"},
		Action:   ActionWrite,
		Context:  RequestContext{RowsAffected: 50},
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("50 rows should be allowed, got %q", decision.Effect)
	}

	// 150 rows should be denied
	req.Context.RowsAffected = 150
	decision = engine.Evaluate(req)
	if decision.Effect != EffectDeny {
		t.Errorf("150 rows should be denied, got %q", decision.Effect)
	}
}

func TestDryRunMode(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: deny-all
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        message: "All writes denied"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Without dry run, should deny
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DryRun: false})
	req := Request{
		Resource: RequestResource{Type: "database", Name: "test-db"},
		Action:   ActionWrite,
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectDeny {
		t.Errorf("Without dry-run, should deny, got %q", decision.Effect)
	}

	// With dry run, should allow but preserve message
	engine = NewEngine(EngineConfig{PolicyConfig: cfg, DryRun: true})
	decision = engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("With dry-run, should allow, got %q", decision.Effect)
	}
	if decision.Message == "" || decision.Message[0:9] != "[DRY RUN]" {
		t.Errorf("Dry-run message should be prefixed, got %q", decision.Message)
	}
}

func TestNamePatternMatching(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: prod-pattern
    resources:
      - type: database
        match:
          name_pattern: "prod-*"
    rules:
      - action: destructive
        effect: deny
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// prod-db should match
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionDestructive,
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectDeny {
		t.Errorf("prod-db should be denied, got %q", decision.Effect)
	}

	// dev-db should not match (default deny)
	req.Resource.Name = "dev-db"
	decision = engine.Evaluate(req)
	// Still denied by default, but different policy
	if decision.PolicyName != "default" {
		t.Errorf("dev-db should not match prod pattern, got policy %q", decision.PolicyName)
	}
}

func TestMaxXactAgeSecs_Deny(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: xact-age-limit
    resources:
      - type: database
    rules:
      - action: destructive
        effect: allow
        conditions:
          max_xact_age_secs: 1800
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionDestructive,
		Context:  RequestContext{XactAgeSecs: 7200}, // 2 hours — exceeds 30 min limit
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectDeny {
		t.Errorf("xact age 7200s > limit 1800s should be denied, got %q", decision.Effect)
	}
	if decision.PolicyName != "xact-age-limit" {
		t.Errorf("expected policy 'xact-age-limit', got %q", decision.PolicyName)
	}
}

func TestMaxXactAgeSecs_Allow(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: xact-age-limit
    resources:
      - type: database
    rules:
      - action: destructive
        effect: allow
        conditions:
          max_xact_age_secs: 1800
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionDestructive,
		Context:  RequestContext{XactAgeSecs: 60}, // 1 minute — within limit
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("xact age 60s < limit 1800s should be allowed, got %q", decision.Effect)
	}
}

func TestMaxXactAgeSecs_ZeroDisabled(t *testing.T) {
	// max_xact_age_secs: 0 means disabled — any age is allowed.
	yamlConfig := `
version: "1"
policies:
  - name: no-xact-limit
    resources:
      - type: database
    rules:
      - action: destructive
        effect: allow
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionDestructive,
		Context:  RequestContext{XactAgeSecs: 999999}, // huge age, but no limit set
	}
	decision := engine.Evaluate(req)
	if decision.Effect != EffectAllow {
		t.Errorf("no limit configured, should allow any age, got %q", decision.Effect)
	}
}

// ── Purpose-based conditions ──────────────────────────────────────────────────

func TestAllowedPurposes_Allow(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: pii-purpose-guard
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
        conditions:
          allowed_purposes: [diagnostic, compliance]
        message: "PII data requires diagnostic or compliance purpose"
      - action: read
        effect: deny
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// diagnostic purpose → first rule matches → allow
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionRead,
		Context:  RequestContext{Purpose: "diagnostic"},
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("diagnostic purpose should be allowed, got %q", decision.Effect)
	}

	// compliance purpose → also allowed
	req.Context.Purpose = "compliance"
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("compliance purpose should be allowed, got %q", decision.Effect)
	}
}

func TestAllowedPurposes_Deny(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: pii-purpose-guard
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
        conditions:
          allowed_purposes: [diagnostic, compliance]
      - action: read
        effect: deny
        message: "purpose not allowed"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// no purpose → first rule condition fails → falls through to deny
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionRead,
		Context:  RequestContext{},
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
		t.Errorf("missing purpose should be denied, got %q", decision.Effect)
	}

	// wrong purpose → same result
	req.Context.Purpose = "remediation"
	if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
		t.Errorf("wrong purpose should be denied, got %q", decision.Effect)
	}
}

func TestBlockedPurposes_Deny(t *testing.T) {
	// blocked_purposes on an allow rule: allow unless purpose is blocked.
	// The condition forces deny when the purpose IS in the blocked list.
	yamlConfig := `
version: "1"
policies:
  - name: diagnostic-readonly
    resources:
      - type: database
    rules:
      - action: [write, destructive]
        effect: allow
        conditions:
          blocked_purposes: [diagnostic]
        message: "Diagnostic purpose is read-only"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	// diagnostic purpose → condition blocks → deny
	req := Request{
		Resource: RequestResource{Type: "database", Name: "prod-db"},
		Action:   ActionWrite,
		Context:  RequestContext{Purpose: "diagnostic"},
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
		t.Errorf("write with diagnostic purpose should be denied, got %q", decision.Effect)
	}

	// remediation purpose → not blocked → allow
	req.Context.Purpose = "remediation"
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("write with remediation purpose should be allowed, got %q", decision.Effect)
	}

	// no purpose → not blocked → allow
	req.Context.Purpose = ""
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("write with no purpose should be allowed (only diagnostic is blocked), got %q", decision.Effect)
	}
}

func TestBlockedPurposes_MultipleBlocked(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: write-restrictions
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          blocked_purposes: [diagnostic, research]
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	for _, blocked := range []string{"diagnostic", "research"} {
		req := Request{
			Resource: RequestResource{Type: "database", Name: "db"},
			Action:   ActionWrite,
			Context:  RequestContext{Purpose: blocked},
		}
		if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
			t.Errorf("purpose %q should be blocked, got %q", blocked, decision.Effect)
		}
	}

	req := Request{
		Resource: RequestResource{Type: "database", Name: "db"},
		Action:   ActionWrite,
		Context:  RequestContext{Purpose: "maintenance"},
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("maintenance purpose should be allowed, got %q", decision.Effect)
	}
}

func TestPurpose_EmergencyBreakGlass(t *testing.T) {
	// High-priority emergency policy allows any action; lower-priority deny blocks it.
	yamlConfig := `
version: "1"
policies:
  - name: emergency-override
    priority: 200
    principals:
      - role: oncall
    resources:
      - type: database
    rules:
      - action: [read, write, destructive]
        effect: allow
        conditions:
          allowed_purposes: [emergency]

  - name: production-protection
    priority: 100
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: destructive
        effect: deny
        message: "Destructive ops on production are blocked"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// oncall + emergency purpose → higher-priority rule allows despite production-protection
	req := Request{
		Principal: RequestPrincipal{Roles: []string{"oncall"}},
		Resource:  RequestResource{Type: "database", Name: "prod-db", Tags: []string{"production"}},
		Action:    ActionDestructive,
		Context:   RequestContext{Purpose: "emergency"},
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("oncall + emergency should override production protection, got %q: %s", decision.Effect, decision.Message)
	}

	// non-oncall + emergency → emergency policy principal doesn't match → still denied
	req.Principal = RequestPrincipal{Roles: []string{"developer"}}
	if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
		t.Errorf("developer + emergency should still be denied on production, got %q", decision.Effect)
	}
}

// ── Sensitivity-based resource matching ──────────────────────────────────────

func TestSensitivityMatching_Allow(t *testing.T) {
	yamlConfig := `
version: "1"
policies:
  - name: pii-protection
    resources:
      - type: database
        match:
          sensitivity: [pii]
    rules:
      - action: read
        effect: allow
        conditions:
          allowed_purposes: [diagnostic, compliance]
      - action: read
        effect: deny
        message: "PII access requires declared purpose"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	// PII resource + diagnostic purpose → allow
	req := Request{
		Resource: RequestResource{Type: "database", Name: "customers", Sensitivity: []string{"pii"}},
		Action:   ActionRead,
		Context:  RequestContext{Purpose: "diagnostic"},
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("pii + diagnostic should be allowed, got %q", decision.Effect)
	}

	// PII resource + no purpose → deny
	req.Context.Purpose = ""
	if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
		t.Errorf("pii + no purpose should be denied, got %q", decision.Effect)
	}

	// Non-PII resource + no purpose → policy doesn't match → default allow
	req.Resource.Sensitivity = []string{"internal"}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("non-pii resource: policy should not match, got %q", decision.Effect)
	}
}

func TestSensitivityMatching_MultipleRequired(t *testing.T) {
	// Policy requires both "pii" AND "critical" to match.
	yamlConfig := `
version: "1"
policies:
  - name: critical-pii
    resources:
      - type: database
        match:
          sensitivity: [pii, critical]
    rules:
      - action: destructive
        effect: deny
        message: "No destructive ops on critical PII data"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	// Both pii and critical → policy matches → deny
	req := Request{
		Resource: RequestResource{Type: "database", Name: "db", Sensitivity: []string{"pii", "critical"}},
		Action:   ActionDestructive,
	}
	if decision := engine.Evaluate(req); decision.Effect != EffectDeny {
		t.Errorf("pii+critical should be denied, got %q", decision.Effect)
	}

	// Only pii (missing critical) → policy doesn't match → default allow
	req.Resource.Sensitivity = []string{"pii"}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("pii only (no critical) should not match policy, got %q", decision.Effect)
	}

	// Only critical (missing pii) → policy doesn't match → default allow
	req.Resource.Sensitivity = []string{"critical"}
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("critical only (no pii) should not match policy, got %q", decision.Effect)
	}

	// No sensitivity → policy doesn't match → default allow
	req.Resource.Sensitivity = nil
	if decision := engine.Evaluate(req); decision.Effect != EffectAllow {
		t.Errorf("no sensitivity: policy should not match, got %q", decision.Effect)
	}
}

func TestSensitivityAndPurpose_Combined(t *testing.T) {
	// Sensitivity matching + purpose condition together.
	yamlConfig := `
version: "1"
policies:
  - name: sensitive-write-guard
    resources:
      - type: database
        match:
          sensitivity: [sensitive]
    rules:
      - action: write
        effect: allow
        conditions:
          allowed_purposes: [maintenance, remediation]
      - action: write
        effect: deny
        message: "Writes to sensitive resources require maintenance or remediation purpose"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg, DefaultEffect: EffectAllow})

	cases := []struct {
		purpose string
		want    Effect
	}{
		{"maintenance", EffectAllow},
		{"remediation", EffectAllow},
		{"diagnostic", EffectDeny},
		{"", EffectDeny},
	}
	for _, tc := range cases {
		req := Request{
			Resource: RequestResource{Type: "database", Name: "db", Sensitivity: []string{"sensitive"}},
			Action:   ActionWrite,
			Context:  RequestContext{Purpose: tc.purpose},
		}
		if decision := engine.Evaluate(req); decision.Effect != tc.want {
			t.Errorf("purpose=%q: want %q, got %q", tc.purpose, tc.want, decision.Effect)
		}
	}
}

func TestSensitivityAndPrincipal_Combined(t *testing.T) {
	// DBA role can write to PII resources; others cannot.
	yamlConfig := `
version: "1"
policies:
  - name: pii-dba-only
    principals:
      - role: dba
    resources:
      - type: database
        match:
          sensitivity: [pii]
    rules:
      - action: write
        effect: allow

  - name: pii-others-deny
    resources:
      - type: database
        match:
          sensitivity: [pii]
    rules:
      - action: write
        effect: deny
        message: "Only DBAs can write to PII resources"
`
	cfg, err := Load([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	dbaReq := Request{
		Principal: RequestPrincipal{Roles: []string{"dba"}},
		Resource:  RequestResource{Type: "database", Name: "db", Sensitivity: []string{"pii"}},
		Action:    ActionWrite,
	}
	if decision := engine.Evaluate(dbaReq); decision.Effect != EffectAllow {
		t.Errorf("DBA should be allowed on PII resource, got %q", decision.Effect)
	}

	devReq := Request{
		Principal: RequestPrincipal{Roles: []string{"developer"}},
		Resource:  RequestResource{Type: "database", Name: "db", Sensitivity: []string{"pii"}},
		Action:    ActionWrite,
	}
	if decision := engine.Evaluate(devReq); decision.Effect != EffectDeny {
		t.Errorf("developer should be denied on PII resource, got %q", decision.Effect)
	}
}

func TestToolNameMatching_ExactTool(t *testing.T) {
	yaml := `
version: "1"
policies:
  - name: deny-terminate
    resources:
      - type: database
        match:
          tool: terminate_connection
    rules:
      - action: destructive
        effect: deny
        message: "terminate_connection is disabled by policy"
`
	cfg, err := Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// terminate_connection should be denied.
	denied := engine.Evaluate(Request{
		Resource: RequestResource{Type: "database", ToolName: "terminate_connection"},
		Action:   ActionDestructive,
	})
	if denied.Effect != EffectDeny {
		t.Errorf("terminate_connection should be denied, got %q", denied.Effect)
	}
	if denied.Message != "terminate_connection is disabled by policy" {
		t.Errorf("unexpected message: %q", denied.Message)
	}

	// cancel_query has the same action class but different tool — policy should not match.
	notDenied := engine.Evaluate(Request{
		Resource: RequestResource{Type: "database", ToolName: "cancel_query"},
		Action:   ActionDestructive,
	})
	// Default engine deny applies (no policy matches), but NOT from the tool policy.
	_ = notDenied // no policy matches → default deny, that's fine; what matters is no message from the tool policy
	if notDenied.Message == "terminate_connection is disabled by policy" {
		t.Error("cancel_query should not match the terminate_connection tool policy")
	}
}

func TestToolNameMatching_GlobPattern(t *testing.T) {
	yaml := `
version: "1"
policies:
  - name: deny-all-terminate
    resources:
      - type: database
        match:
          tool_pattern: "terminate_*"
    rules:
      - action: destructive
        effect: deny
        message: "all terminate tools disabled"
`
	cfg, err := Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	for _, toolName := range []string{"terminate_connection", "terminate_idle_connections"} {
		d := engine.Evaluate(Request{
			Resource: RequestResource{Type: "database", ToolName: toolName},
			Action:   ActionDestructive,
		})
		if d.Effect != EffectDeny {
			t.Errorf("%s: expected deny, got %q", toolName, d.Effect)
		}
	}

	// cancel_query does not match "terminate_*".
	d := engine.Evaluate(Request{
		Resource: RequestResource{Type: "database", ToolName: "cancel_query"},
		Action:   ActionWrite,
	})
	if d.Message == "all terminate tools disabled" {
		t.Error("cancel_query should not match terminate_* pattern")
	}
}

func TestToolNameMatching_NoToolName_PolicySkipped(t *testing.T) {
	// When request has no ToolName set, a policy with tool: X should NOT match.
	yaml := `
version: "1"
policies:
  - name: deny-terminate
    resources:
      - type: database
        match:
          tool: terminate_connection
    rules:
      - action: destructive
        effect: deny
`
	cfg, err := Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	engine := NewEngine(EngineConfig{PolicyConfig: cfg})

	// No ToolName in request → policy should not match → default deny (not from this policy).
	d := engine.Evaluate(Request{
		Resource: RequestResource{Type: "database"}, // no ToolName
		Action:   ActionDestructive,
	})
	if d.PolicyName == "deny-terminate" {
		t.Errorf("policy with tool: should not match request with no ToolName, but got policy %q", d.PolicyName)
	}
}

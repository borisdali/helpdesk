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

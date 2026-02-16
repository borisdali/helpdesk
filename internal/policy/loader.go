package policy

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// LoadFile loads a policy configuration from a YAML file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}
	return Load(data)
}

// Load parses policy configuration from YAML data.
func Load(data []byte) (*Config, error) {
	// Expand environment variables in the YAML
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse policy YAML: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate policy: %w", err)
	}

	// Sort policies by priority (higher first)
	sort.Slice(cfg.Policies, func(i, j int) bool {
		return cfg.Policies[i].Priority > cfg.Policies[j].Priority
	})

	return &cfg, nil
}

// validate checks the policy configuration for errors.
func validate(cfg *Config) error {
	if cfg.Version == "" {
		cfg.Version = "1"
	}

	seenNames := make(map[string]bool)
	for i, p := range cfg.Policies {
		if p.Name == "" {
			return fmt.Errorf("policy %d: name is required", i)
		}
		if seenNames[p.Name] {
			return fmt.Errorf("policy %d: duplicate name %q", i, p.Name)
		}
		seenNames[p.Name] = true

		if len(p.Resources) == 0 {
			return fmt.Errorf("policy %q: at least one resource is required", p.Name)
		}

		if len(p.Rules) == 0 {
			return fmt.Errorf("policy %q: at least one rule is required", p.Name)
		}

		for j, r := range p.Rules {
			if len(r.Action) == 0 {
				return fmt.Errorf("policy %q rule %d: action is required", p.Name, j)
			}
			if r.Effect == "" {
				return fmt.Errorf("policy %q rule %d: effect is required", p.Name, j)
			}
			if r.Effect != EffectAllow && r.Effect != EffectDeny && r.Effect != EffectRequireApproval {
				return fmt.Errorf("policy %q rule %d: invalid effect %q", p.Name, j, r.Effect)
			}
		}
	}

	return nil
}

// DefaultConfig returns a minimal default policy configuration.
// This is used when no policy file is configured.
func DefaultConfig() *Config {
	return &Config{
		Version: "1",
		Policies: []Policy{
			{
				Name:        "default-allow-read",
				Description: "Allow all read operations by default",
				Resources: []Resource{
					{Type: "database"},
					{Type: "kubernetes"},
				},
				Rules: []Rule{
					{
						Action: ActionMatcher{ActionRead},
						Effect: EffectAllow,
					},
					{
						Action:  ActionMatcher{ActionWrite},
						Effect:  EffectRequireApproval,
						Message: "Write operations require approval",
					},
					{
						Action:  ActionMatcher{ActionDestructive},
						Effect:  EffectDeny,
						Message: "Destructive operations are not allowed by default policy",
					},
				},
			},
		},
	}
}

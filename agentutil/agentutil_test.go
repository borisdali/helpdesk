package agentutil

import (
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
)

func TestApplyCardOptions_Empty(t *testing.T) {
	card := &a2a.AgentCard{
		Name:    "test",
		Version: "0.1.0",
	}
	applyCardOptions(card, CardOptions{})

	if card.Version != "0.1.0" {
		t.Errorf("Version changed to %q, expected no change", card.Version)
	}
}

func TestApplyCardOptions_Version(t *testing.T) {
	card := &a2a.AgentCard{Name: "test", Version: "0.1.0"}
	applyCardOptions(card, CardOptions{Version: "2.0.0"})
	if card.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", card.Version, "2.0.0")
	}
}

func TestApplyCardOptions_DocumentationURL(t *testing.T) {
	card := &a2a.AgentCard{Name: "test"}
	applyCardOptions(card, CardOptions{DocumentationURL: "https://docs.example.com"})
	if card.DocumentationURL != "https://docs.example.com" {
		t.Errorf("DocumentationURL = %q, want %q", card.DocumentationURL, "https://docs.example.com")
	}
}

func TestApplyCardOptions_Provider(t *testing.T) {
	card := &a2a.AgentCard{Name: "test"}
	provider := &a2a.AgentProvider{Org: "TestOrg", URL: "https://test.org"}
	applyCardOptions(card, CardOptions{Provider: provider})
	if card.Provider == nil {
		t.Fatal("Provider should be set")
	}
	if card.Provider.Org != "TestOrg" {
		t.Errorf("Provider.Org = %q, want %q", card.Provider.Org, "TestOrg")
	}
}

func TestApplyCardOptions_SkillTagsMerged(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "skill-a", Tags: []string{"existing"}},
			{ID: "skill-b", Tags: []string{"b-tag"}},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillTags: map[string][]string{
			"skill-a": {"new-tag-1", "new-tag-2"},
		},
	})
	if len(card.Skills[0].Tags) != 3 {
		t.Fatalf("skill-a tags = %v, want 3 tags", card.Skills[0].Tags)
	}
	if card.Skills[0].Tags[0] != "existing" || card.Skills[0].Tags[1] != "new-tag-1" {
		t.Errorf("skill-a tags = %v, unexpected order", card.Skills[0].Tags)
	}
	// skill-b should be unchanged.
	if len(card.Skills[1].Tags) != 1 {
		t.Errorf("skill-b tags = %v, expected unchanged", card.Skills[1].Tags)
	}
}

func TestApplyCardOptions_SkillExamples(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "skill-a", Examples: []string{"old example"}},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillExamples: map[string][]string{
			"skill-a": {"example 1", "example 2"},
		},
	})
	if len(card.Skills[0].Examples) != 2 {
		t.Fatalf("skill-a examples = %v, want 2", card.Skills[0].Examples)
	}
	if card.Skills[0].Examples[0] != "example 1" {
		t.Errorf("examples[0] = %q, want %q", card.Skills[0].Examples[0], "example 1")
	}
}

// Package toolregistry provides an immutable catalog of tools discovered from
// agent cards. It is used by the gateway to validate direct tool calls and to
// build the fleet job planner's tool catalog.
package toolregistry

import (
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"

	"helpdesk/internal/audit"
)

// ToolEntry describes one registered tool.
type ToolEntry struct {
	Name        string         `json:"name"`
	Agent       string         `json:"agent"`        // "database" or "k8s"
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"` // JSON Schema properties
	ActionClass string         `json:"action_class"` // "read", "write", "destructive"
}

// Registry is an immutable catalog of tools built from agent cards.
type Registry struct {
	tools  []ToolEntry
	byName map[string]ToolEntry
}

// New builds a Registry from a flat list of entries.
func New(entries []ToolEntry) *Registry {
	byName := make(map[string]ToolEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	return &Registry{
		tools:  entries,
		byName: byName,
	}
}

// Get returns the entry for the given tool name (false if not found).
func (r *Registry) Get(name string) (ToolEntry, bool) {
	e, ok := r.byName[name]
	return e, ok
}

// List returns all entries.
func (r *Registry) List() []ToolEntry {
	return r.tools
}

// ListByAgent returns entries for the given agent name.
func (r *Registry) ListByAgent(agent string) []ToolEntry {
	var result []ToolEntry
	for _, e := range r.tools {
		if e.Agent == agent {
			result = append(result, e)
		}
	}
	return result
}

// Validate checks that all required parameters are present in args.
// It reads the "required" []string field from InputSchema if present.
// Returns nil if args is nil (lenient for read tools) or if no required
// parameters are declared.
func (r *Registry) Validate(toolName string, args map[string]any) error {
	entry, ok := r.byName[toolName]
	if !ok {
		return fmt.Errorf("unknown tool: %s", toolName)
	}

	if entry.InputSchema == nil {
		return nil
	}

	requiredRaw, ok := entry.InputSchema["required"]
	if !ok {
		return nil
	}

	var requiredKeys []string
	switch v := requiredRaw.(type) {
	case []string:
		requiredKeys = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				requiredKeys = append(requiredKeys, s)
			}
		}
	default:
		return nil
	}

	if len(requiredKeys) == 0 {
		return nil
	}

	// If args is nil, only report missing if there are required keys.
	for _, key := range requiredKeys {
		if args == nil {
			return fmt.Errorf("tool %s: missing required parameter %q", toolName, key)
		}
		if _, present := args[key]; !present {
			return fmt.Errorf("tool %s: missing required parameter %q", toolName, key)
		}
	}

	return nil
}

// agentShortName maps internal agent names to their registry short names.
// Skills whose IDs don't match these are skipped (e.g., the root agent skill).
var agentShortName = map[string]string{
	"postgres_database_agent": "database",
	"k8s_agent":               "k8s",
	"incident_agent":          "incident",
	"research_agent":          "research",
}

// Build constructs a Registry from the gateway's discovered agents.
// agentCards maps agent name → *a2a.AgentCard.
// classification is audit.ToolClassification.
func Build(agentCards map[string]*a2a.AgentCard, classification map[string]audit.ActionClass) *Registry {
	var entries []ToolEntry

	for agentName, card := range agentCards {
		if card == nil {
			continue
		}
		shortName, ok := agentShortName[agentName]
		if !ok {
			shortName = agentName
		}

		for _, skill := range card.Skills {
			// Skills named exactly after the agent (root skill) have no dash in ID,
			// or the ID equals just the agent name. Skip them — they represent the
			// agent itself, not a specific tool.
			toolName := extractToolName(agentName, skill.ID)
			if toolName == "" {
				continue
			}

			actionClass := string(audit.ActionUnknown)
			if class, found := classification[toolName]; found {
				actionClass = string(class)
			}

			desc := skill.Description
			if desc == "" {
				desc = skill.Name
			}

			entries = append(entries, ToolEntry{
				Name:        toolName,
				Agent:       shortName,
				Description: desc,
				ActionClass: actionClass,
			})
		}
	}

	return New(entries)
}

// extractToolName derives the bare tool name from the skill ID.
// Skill IDs follow the pattern: "agentName-toolName" (e.g. "postgres_database_agent-check_connection").
// Returns "" for the root agent skill (ID == agentName with no suffix).
func extractToolName(agentName, skillID string) string {
	prefix := agentName + "-"
	if strings.HasPrefix(skillID, prefix) {
		return strings.TrimPrefix(skillID, prefix)
	}
	// Also handle cases where the agent name uses underscores and skill ID uses the same.
	return ""
}

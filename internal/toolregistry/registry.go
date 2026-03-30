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
	Name          string         `json:"name"`
	Agent         string         `json:"agent"`                  // "database" or "k8s"
	Description   string         `json:"description"`
	InputSchema   map[string]any `json:"input_schema,omitempty"` // JSON Schema properties
	ActionClass   string         `json:"action_class"`           // "read", "write", "destructive"
	FleetEligible bool           `json:"fleet_eligible"`         // hard gate: planner only sees these
	Capabilities  []string       `json:"capabilities,omitempty"` // controlled vocabulary (Cap* constants)
	Supersedes    []string       `json:"supersedes,omitempty"`   // tool names this one makes redundant
	// AgentVersion is the agent's version string at discovery time (from card.Version).
	AgentVersion string `json:"agent_version,omitempty"`
	// SchemaFingerprint is the sha256[:12] hex of the tool's Declaration().Parameters JSON.
	// Populated from "schema_hash:<fingerprint>" tags serialized by the agent at startup.
	SchemaFingerprint string `json:"schema_fingerprint,omitempty"`
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

// ListFleetEligible returns only the tools marked as fleet-eligible.
// The fleet planner calls this instead of List so non-fleet tools are invisible.
func (r *Registry) ListFleetEligible() []ToolEntry {
	var result []ToolEntry
	for _, e := range r.tools {
		if e.FleetEligible {
			result = append(result, e)
		}
	}
	return result
}

// ListByCapability returns all tools that declare the given capability.
func (r *Registry) ListByCapability(cap string) []ToolEntry {
	var result []ToolEntry
	for _, e := range r.tools {
		for _, c := range e.Capabilities {
			if c == cap {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

// ResolveSuperseded removes from names any tool that is superseded by another
// tool already present in names. This is deterministic post-processing: even if
// the LLM selects redundant tools, the dominant one wins.
//
// Example: if get_status_summary declares Supersedes=["get_server_info","get_connection_stats"]
// and all three are in names, the result will contain only get_status_summary.
func (r *Registry) ResolveSuperseded(names []string) []string {
	// Index the input names for O(1) lookup.
	inSet := make(map[string]bool, len(names))
	for _, n := range names {
		inSet[n] = true
	}

	// For each tool in the input that declares Supersedes, mark dominated names.
	dominated := make(map[string]bool)
	for _, n := range names {
		e, ok := r.byName[n]
		if !ok {
			continue
		}
		for _, sup := range e.Supersedes {
			if inSet[sup] {
				dominated[sup] = true
			}
		}
	}

	if len(dominated) == 0 {
		return names
	}

	result := make([]string, 0, len(names))
	for _, n := range names {
		if !dominated[n] {
			result = append(result, n)
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
// agentSchemas maps agent name → (tool name → JSON Schema properties); pass nil if unavailable.
// classification is audit.ToolClassification.
func Build(agentCards map[string]*a2a.AgentCard, agentSchemas map[string]map[string]map[string]any, classification map[string]audit.ActionClass) *Registry {
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

			fleetEligible, caps, supersedes, schemaFingerprint := parseSkillTags(skill.Tags)

			var inputSchema map[string]any
			if agentSchemas != nil {
				if toolSchemas, ok := agentSchemas[agentName]; ok {
					inputSchema = toolSchemas[toolName]
				}
			}

			entries = append(entries, ToolEntry{
				Name:              toolName,
				Agent:             shortName,
				Description:       desc,
				InputSchema:       inputSchema,
				ActionClass:       actionClass,
				FleetEligible:     fleetEligible,
				Capabilities:      caps,
				Supersedes:        supersedes,
				AgentVersion:      card.Version,
				SchemaFingerprint: schemaFingerprint,
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

// parseSkillTags extracts typed taxonomy metadata from the key:value tag strings
// that applyCardOptions serializes from the typed CardOptions fields.
func parseSkillTags(tags []string) (fleetEligible bool, caps, supersedes []string, schemaFingerprint string) {
	for _, tag := range tags {
		switch {
		case tag == "fleet:true":
			fleetEligible = true
		case strings.HasPrefix(tag, "cap:"):
			caps = append(caps, strings.TrimPrefix(tag, "cap:"))
		case strings.HasPrefix(tag, "supersedes:"):
			supersedes = append(supersedes, strings.TrimPrefix(tag, "supersedes:"))
		case strings.HasPrefix(tag, "schema_hash:"):
			schemaFingerprint = strings.TrimPrefix(tag, "schema_hash:")
		}
	}
	return fleetEligible, caps, supersedes, schemaFingerprint
}

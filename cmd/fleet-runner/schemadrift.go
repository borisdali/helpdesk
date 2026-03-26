package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"helpdesk/internal/fleet"
	"helpdesk/internal/toolregistry"
)

// SchemaDriftResult describes the drift state of a single tool.
type SchemaDriftResult struct {
	Tool               string
	Snapshot           fleet.ToolSnapshot
	CurrentFingerprint string
	CurrentVersion     string
	FingerprintChanged bool
	VersionChanged     bool
}

// CheckSchemaDrift compares job file snapshots against live tool registry entries.
//
// Policy behaviour:
//   - "abort" (default): return an error if any fingerprint changed, or if
//     snapshots is nil/empty.
//   - "warn": log a warning for any drift but return nil.
//   - "ignore": silently return nil for all cases.
//
// Returns (results, nil) on success; results is non-nil only when drift was found.
func CheckSchemaDrift(snapshots map[string]fleet.ToolSnapshot, liveTools []toolregistry.ToolEntry, policy string) ([]SchemaDriftResult, error) {
	if policy == "ignore" {
		return nil, nil
	}

	if len(snapshots) == 0 {
		if policy == "abort" {
			return nil, fmt.Errorf("job has no tool_snapshots: cannot verify schema drift\n  → run the planner again to generate a fresh job, or set --schema-drift=ignore to skip this check")
		}
		return nil, nil
	}

	// Build a live index of tools by name.
	live := make(map[string]toolregistry.ToolEntry, len(liveTools))
	for _, t := range liveTools {
		live[t.Name] = t
	}

	var results []SchemaDriftResult
	for toolName, snap := range snapshots {
		entry, exists := live[toolName]
		if !exists {
			result := SchemaDriftResult{
				Tool:               toolName,
				Snapshot:           snap,
				FingerprintChanged: snap.SchemaFingerprint != "",
				VersionChanged:     snap.AgentVersion != "",
			}
			results = append(results, result)
			msg := fmt.Sprintf("tool %q no longer exists in live registry", toolName)
			if policy == "abort" {
				return results, fmt.Errorf("%s\n  planned against: fingerprint=%s  version=%s  captured=%s\n  → aborting (set strategy.schema_drift=warn to override)",
					msg, snap.SchemaFingerprint, snap.AgentVersion, snap.CapturedAt.Format("2006-01-02T15:04:05Z"))
			}
			slog.Warn("schema drift: "+msg,
				"tool", toolName,
				"planned_fingerprint", snap.SchemaFingerprint,
				"planned_version", snap.AgentVersion,
			)
			continue
		}

		fpChanged := snap.SchemaFingerprint != "" && entry.SchemaFingerprint != "" && snap.SchemaFingerprint != entry.SchemaFingerprint
		verChanged := snap.AgentVersion != "" && entry.AgentVersion != "" && snap.AgentVersion != entry.AgentVersion

		if !fpChanged && !verChanged {
			continue
		}

		result := SchemaDriftResult{
			Tool:               toolName,
			Snapshot:           snap,
			CurrentFingerprint: entry.SchemaFingerprint,
			CurrentVersion:     entry.AgentVersion,
			FingerprintChanged: fpChanged,
			VersionChanged:     verChanged,
		}
		results = append(results, result)

		if fpChanged {
			msg := fmt.Sprintf("schema drift detected for tool %q:\n  planned against: fingerprint=%s  version=%s  captured=%s\n  current:         fingerprint=%s  version=%s",
				toolName,
				snap.SchemaFingerprint, snap.AgentVersion, snap.CapturedAt.Format("2006-01-02T15:04:05Z"),
				entry.SchemaFingerprint, entry.AgentVersion,
			)
			if policy == "abort" {
				return results, fmt.Errorf("%s\n  → aborting (set strategy.schema_drift=warn to override)", msg)
			}
			slog.Warn("schema drift: fingerprint changed",
				"tool", toolName,
				"planned_fingerprint", snap.SchemaFingerprint,
				"current_fingerprint", entry.SchemaFingerprint,
				"planned_version", snap.AgentVersion,
				"current_version", entry.AgentVersion,
			)
		} else if verChanged {
			slog.Warn("schema drift: agent version changed (fingerprint unchanged)",
				"tool", toolName,
				"planned_version", snap.AgentVersion,
				"current_version", entry.AgentVersion,
			)
		}
	}

	return results, nil
}

// fetchLiveTools retrieves the live tool list from the gateway registry endpoint.
// Returns an error if the gateway is unreachable or returns a non-200 response.
func fetchLiveTools(gatewayURL string) ([]toolregistry.ToolEntry, error) {
	url := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/tools"
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("failed to reach gateway tools endpoint %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway tools endpoint returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Tools []toolregistry.ToolEntry `json:"tools"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse gateway tools response: %w", err)
	}
	return result.Tools, nil
}

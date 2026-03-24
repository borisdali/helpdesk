package main

import (
	"fmt"

	"helpdesk/internal/fleet"
	"helpdesk/internal/infra"
)

// resolveTargets filters infra.DBServers according to the Targets spec and
// returns the ordered list of server names to process.
func resolveTargets(cfg *infra.Config, targets fleet.Targets) ([]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no infrastructure configuration loaded (set HELPDESK_INFRA_CONFIG)")
	}

	excludeSet := make(map[string]bool, len(targets.Exclude))
	for _, name := range targets.Exclude {
		excludeSet[name] = true
	}

	// Build explicit-name set for fast lookup (matched against map keys).
	nameSet := make(map[string]bool, len(targets.Names))
	for _, name := range targets.Names {
		nameSet[name] = true
	}

	var selected []string
	for serverKey, server := range cfg.DBServers {
		if excludeSet[serverKey] {
			continue
		}

		// Include if explicitly named (by map key).
		if nameSet[serverKey] {
			selected = append(selected, serverKey)
			continue
		}

		// Include if any tag matches.
		if len(targets.Tags) > 0 {
			for _, wantTag := range targets.Tags {
				for _, serverTag := range server.Tags {
					if serverTag == wantTag {
						selected = append(selected, serverKey)
						goto nextServer
					}
				}
			}
		}

	nextServer:
	}

	// If neither tags nor names were specified, select all servers (minus excludes).
	if len(targets.Tags) == 0 && len(targets.Names) == 0 {
		selected = selected[:0]
		for serverKey := range cfg.DBServers {
			if !excludeSet[serverKey] {
				selected = append(selected, serverKey)
			}
		}
	}

	return selected, nil
}

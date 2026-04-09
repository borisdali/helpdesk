package playbooks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"
	"helpdesk/internal/audit"
)

// systemPlaybookYAML is the wire format for YAML playbook files. Explicit yaml: tags
// are used to avoid dependency on audit.Playbook gaining yaml tags.
type systemPlaybookYAML struct {
	SeriesID        string   `yaml:"series_id"`
	Name            string   `yaml:"name"`
	Version         string   `yaml:"version"`
	ProblemClass    string   `yaml:"problem_class"`
	Author          string   `yaml:"author"`
	Description     string   `yaml:"description"`
	Symptoms        []string `yaml:"symptoms"`
	Guidance        string   `yaml:"guidance"`
	Escalation      []string `yaml:"escalation"`
	TargetHints     []string `yaml:"target_hints"`
	EntryPoint      bool     `yaml:"entry_point"`
	EscalatesTo     []string `yaml:"escalates_to"`
	RequiresEvidence []string `yaml:"requires_evidence"`
	ExecutionMode   string   `yaml:"execution_mode"`
}

// SeedSystemPlaybooks reads all embedded *.yaml files and inserts them into the
// store as system playbooks. It is idempotent:
//   - If the exact (series_id, version) already exists, the file is skipped.
//   - If the series exists but this version is new, it is inserted as inactive
//     so customers can review and promote it when ready.
//   - If the series is brand new, the first version is inserted as active.
//
// Errors from individual files are logged but do not abort the remaining files.
// Returns the first encountered fatal error (store failures), or nil.
func SeedSystemPlaybooks(ctx context.Context, store *audit.PlaybookStore) error {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read embedded playbooks: %w", err)
	}

	seeded, skipped := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := FS.ReadFile(entry.Name())
		if err != nil {
			slog.Warn("playbooks: failed to read embedded file", "file", entry.Name(), "err", err)
			continue
		}

		var y systemPlaybookYAML
		if err := yaml.Unmarshal(data, &y); err != nil {
			slog.Warn("playbooks: failed to parse YAML", "file", entry.Name(), "err", err)
			continue
		}
		if y.SeriesID == "" || y.Name == "" {
			slog.Warn("playbooks: skipping file with missing series_id or name", "file", entry.Name())
			continue
		}

		// Check idempotency: list all versions of this series (including inactive and system).
		existing, err := store.List(ctx, audit.PlaybookListQuery{
			SeriesID:      y.SeriesID,
			ActiveOnly:    false,
			IncludeSystem: true,
		})
		if err != nil {
			return fmt.Errorf("list series %q: %w", y.SeriesID, err)
		}

		// Skip if this exact version already exists.
		for _, pb := range existing {
			if pb.Version == y.Version {
				slog.Debug("playbooks: skipping already-seeded version",
					"series", y.SeriesID, "version", y.Version)
				skipped++
				goto nextFile
			}
		}

		{
			// First version of a series → active; subsequent versions → inactive.
			isActive := len(existing) == 0
			pb := &audit.Playbook{
				SeriesID:        y.SeriesID,
				Name:            y.Name,
				Version:         y.Version,
				ProblemClass:    y.ProblemClass,
				Author:          y.Author,
				Description:     y.Description,
				Symptoms:        y.Symptoms,
				Guidance:        y.Guidance,
				Escalation:      y.Escalation,
				TargetHints:     y.TargetHints,
				EntryPoint:      y.EntryPoint,
				EscalatesTo:     y.EscalatesTo,
				RequiresEvidence: y.RequiresEvidence,
				ExecutionMode:   y.ExecutionMode,
				IsSystem:        true,
				IsActive:        isActive,
				Source:          "system",
			}
			if err := store.Create(ctx, pb); err != nil {
				return fmt.Errorf("seed playbook %q v%s: %w", y.SeriesID, y.Version, err)
			}
			slog.Info("playbooks: seeded system playbook",
				"name", y.Name, "series", y.SeriesID, "version", y.Version, "active", isActive)
			seeded++
		}

	nextFile:
	}

	slog.Info("playbooks: seed complete", "seeded", seeded, "skipped", skipped)
	return nil
}

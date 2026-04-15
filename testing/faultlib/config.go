package faultlib

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"helpdesk/testing/catalog"
)

// LoadCatalog reads and parses the failure catalog YAML file.
// The source field on each failure is set to "custom".
func LoadCatalog(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog: %v", err)
	}
	return LoadCatalogFromBytes(data, "custom")
}

// LoadBuiltinCatalog parses the embedded built-in catalog.
// Each failure's Source is stamped as "builtin".
func LoadBuiltinCatalog() (*Catalog, error) {
	return LoadCatalogFromBytes(catalog.BuiltinYAML, "builtin")
}

// LoadCatalogFromBytes parses YAML bytes and stamps each failure with the given
// source label ("builtin" or "custom"). The version field check is skipped for
// custom catalogs so customers may omit it.
func LoadCatalogFromBytes(data []byte, source string) (*Catalog, error) {
	var c Catalog
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing catalog: %v", err)
	}
	if source == "builtin" && c.Version == "" {
		return nil, fmt.Errorf("built-in catalog missing version field")
	}
	for i := range c.Failures {
		c.Failures[i].Source = source
	}
	return &c, nil
}

// LoadAndMergeCatalogs loads the built-in catalog and appends each custom
// catalog file. All duplicate-ID errors are collected before returning (fail-all,
// not fail-fast). A duplicate is any ID that appears more than once across the
// combined set.
func LoadAndMergeCatalogs(customPaths []string) (*Catalog, error) {
	base, err := LoadBuiltinCatalog()
	if err != nil {
		return nil, err
	}
	return mergeCustomInto(base, customPaths)
}

// mergeCustomInto appends faults from each custom file into base and returns
// the merged catalog. All duplicate-ID errors are collected before returning.
func mergeCustomInto(base *Catalog, paths []string) (*Catalog, error) {
	seen := make(map[string]string) // id → source label
	for i := range base.Failures {
		seen[base.Failures[i].ID] = base.Failures[i].Source
	}

	var errs []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading custom catalog %s: %v", path, err)
		}
		custom, err := LoadCatalogFromBytes(data, "custom")
		if err != nil {
			return nil, fmt.Errorf("parsing custom catalog %s: %v", path, err)
		}
		for _, f := range custom.Failures {
			if prev, dup := seen[f.ID]; dup {
				errs = append(errs, fmt.Sprintf("duplicate fault ID %q (first seen in %s, also in %s)", f.ID, prev, path))
				continue
			}
			seen[f.ID] = path
			base.Failures = append(base.Failures, f)
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("catalog merge errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return base, nil
}

// FilterFailures returns failures matching the given categories and/or IDs.
// When cfg.External is true, only faults marked external_compat are included.
func FilterFailures(catalog *Catalog, cfg *HarnessConfig) []Failure {
	if cfg == nil {
		cfg = &HarnessConfig{}
	}
	categories := cfg.Categories
	ids := cfg.FailureIDs

	catSet := make(map[string]bool, len(categories))
	for _, c := range categories {
		catSet[c] = true
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	var result []Failure
	for _, f := range catalog.Failures {
		// External mode: skip faults that don't work without Docker/OS access.
		if cfg.External && !f.ExternalCompat {
			continue
		}
		// Source filter: "builtin" or "custom".
		if cfg.SourceFilter != "" && f.Source != cfg.SourceFilter {
			continue
		}

		if len(categories) == 0 && len(ids) == 0 {
			result = append(result, f)
			continue
		}
		if len(idSet) > 0 && idSet[f.ID] {
			result = append(result, f)
			continue
		}
		if len(catSet) > 0 && catSet[f.Category] {
			result = append(result, f)
		}
	}
	return result
}

// ResolvePrompt replaces template variables in the failure prompt.
func ResolvePrompt(prompt string, cfg *HarnessConfig) string {
	r := strings.NewReplacer(
		"{{connection_string}}", cfg.ConnStr,
		"{{replica_connection_string}}", cfg.ReplicaConnStr,
		"{{kube_context}}", cfg.KubeContext,
	)
	return r.Replace(prompt)
}

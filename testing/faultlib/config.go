package faultlib

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadCatalog reads and parses the failure catalog YAML file.
func LoadCatalog(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog: %v", err)
	}

	var catalog Catalog
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("parsing catalog: %v", err)
	}

	if catalog.Version == "" {
		return nil, fmt.Errorf("catalog missing version field")
	}

	return &catalog, nil
}

// FilterFailures returns failures matching the given categories and/or IDs.
func FilterFailures(catalog *Catalog, categories, ids []string) []Failure {
	if len(categories) == 0 && len(ids) == 0 {
		return catalog.Failures
	}

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

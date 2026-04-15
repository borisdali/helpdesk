package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const builtinMinimum = 27

func TestLoadCatalog_Valid(t *testing.T) {
	// Load the real catalog and verify structure.
	catalogPath := filepath.Join("..", "..", "catalog", "failures.yaml")
	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog error: %v", err)
	}

	if catalog.Version != "1" {
		t.Errorf("Version = %q, want %q", catalog.Version, "1")
	}

	if len(catalog.Failures) < builtinMinimum {
		t.Errorf("Failures count = %d, want >= %d", len(catalog.Failures), builtinMinimum)
	}

	// Spot-check a known failure.
	var found bool
	for _, f := range catalog.Failures {
		if f.ID == "db-max-connections" {
			found = true
			if f.Category != "database" {
				t.Errorf("db-max-connections category = %q, want %q", f.Category, "database")
			}
			if f.Severity != "high" {
				t.Errorf("db-max-connections severity = %q, want %q", f.Severity, "high")
			}
			break
		}
	}
	if !found {
		t.Error("did not find db-max-connections in catalog")
	}
}

func TestLoadCatalog_MissingFile(t *testing.T) {
	_, err := LoadCatalog("/nonexistent/path/failures.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadCatalog_CustomAllowsMissingVersion(t *testing.T) {
	// Custom catalogs (loaded via LoadCatalog) are allowed to omit the version field.
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	data := `failures:
  - id: test-1
    name: Test failure
    category: database
`
	os.WriteFile(path, []byte(data), 0644)

	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog on custom catalog without version: unexpected error: %v", err)
	}
	if cat.Failures[0].Source != "custom" {
		t.Errorf("expected Source=custom, got %q", cat.Failures[0].Source)
	}
}

func TestLoadBuiltinCatalog(t *testing.T) {
	cat, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatalf("LoadBuiltinCatalog error: %v", err)
	}
	if len(cat.Failures) < builtinMinimum {
		t.Errorf("built-in catalog has %d entries, want >= %d", len(cat.Failures), builtinMinimum)
	}
	for _, f := range cat.Failures {
		if f.Source != "builtin" {
			t.Errorf("fault %q has Source=%q, want %q", f.ID, f.Source, "builtin")
		}
	}
}

func TestLoadAndMergeCatalogs_Duplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conflict.yaml")
	// db-max-connections is a known built-in ID.
	os.WriteFile(path, []byte("failures:\n  - id: db-max-connections\n    name: Conflict\n    category: database\n"), 0644)

	_, err := LoadAndMergeCatalogs([]string{path})
	if err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
	if !strings.Contains(err.Error(), "db-max-connections") {
		t.Errorf("error should mention the duplicate ID; got: %v", err)
	}
}

func TestLoadCatalogFromBytes_BuiltinRequiresVersion(t *testing.T) {
	data := []byte("failures:\n  - id: test-1\n    name: Test\n    category: database\n")
	_, err := LoadCatalogFromBytes(data, "builtin")
	if err == nil {
		t.Fatal("expected error for builtin catalog without version, got nil")
	}
}

func TestLoadAndMergeCatalogs_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	os.WriteFile(path, []byte("failures:\n  - id: my-custom-unique-fault\n    name: Custom Fault\n    category: database\n"), 0644)

	cat, err := LoadAndMergeCatalogs([]string{path})
	if err != nil {
		t.Fatalf("LoadAndMergeCatalogs error: %v", err)
	}
	if len(cat.Failures) < builtinMinimum+1 {
		t.Errorf("merged catalog has %d entries, want >= %d", len(cat.Failures), builtinMinimum+1)
	}
	var found bool
	for _, f := range cat.Failures {
		if f.ID == "my-custom-unique-fault" {
			found = true
			if f.Source != "custom" {
				t.Errorf("custom fault Source=%q, want %q", f.Source, "custom")
			}
		}
	}
	if !found {
		t.Error("custom fault not found in merged catalog")
	}
}

func TestLoadAndMergeCatalogs_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.yaml")
	path2 := filepath.Join(dir, "b.yaml")
	os.WriteFile(path1, []byte("failures:\n  - id: custom-fault-a\n    name: A\n    category: database\n"), 0644)
	os.WriteFile(path2, []byte("failures:\n  - id: custom-fault-b\n    name: B\n    category: host\n"), 0644)

	cat, err := LoadAndMergeCatalogs([]string{path1, path2})
	if err != nil {
		t.Fatalf("LoadAndMergeCatalogs error: %v", err)
	}
	if len(cat.Failures) < builtinMinimum+2 {
		t.Errorf("merged catalog has %d entries, want >= %d", len(cat.Failures), builtinMinimum+2)
	}
	ids := make(map[string]bool, len(cat.Failures))
	for _, f := range cat.Failures {
		ids[f.ID] = true
	}
	if !ids["custom-fault-a"] {
		t.Error("custom-fault-a missing from merged catalog")
	}
	if !ids["custom-fault-b"] {
		t.Error("custom-fault-b missing from merged catalog")
	}
}

func TestLoadAndMergeCatalogs_CrossFileDuplicate(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.yaml")
	path2 := filepath.Join(dir, "b.yaml")
	os.WriteFile(path1, []byte("failures:\n  - id: shared-id\n    name: A\n    category: database\n"), 0644)
	os.WriteFile(path2, []byte("failures:\n  - id: shared-id\n    name: B\n    category: database\n"), 0644)

	_, err := LoadAndMergeCatalogs([]string{path1, path2})
	if err == nil {
		t.Fatal("expected error for cross-file duplicate ID, got nil")
	}
	if !strings.Contains(err.Error(), "shared-id") {
		t.Errorf("error should mention the duplicate ID; got: %v", err)
	}
}

func TestFilterFailures_SourceFilter(t *testing.T) {
	cat := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "b1", Category: "database", Source: "builtin"},
			{ID: "b2", Category: "database", Source: "builtin"},
			{ID: "c1", Category: "database", Source: "custom"},
		},
	}

	builtin := FilterFailures(cat, &HarnessConfig{SourceFilter: "builtin"})
	if len(builtin) != 2 {
		t.Errorf("SourceFilter=builtin: got %d, want 2", len(builtin))
	}

	custom := FilterFailures(cat, &HarnessConfig{SourceFilter: "custom"})
	if len(custom) != 1 {
		t.Errorf("SourceFilter=custom: got %d, want 1", len(custom))
	}
	if custom[0].ID != "c1" {
		t.Errorf("SourceFilter=custom: got ID %q, want c1", custom[0].ID)
	}

	all := FilterFailures(cat, &HarnessConfig{SourceFilter: ""})
	if len(all) != 3 {
		t.Errorf("SourceFilter='': got %d, want 3", len(all))
	}
}

func TestLoadCatalog_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("{{bad yaml"), 0644)

	_, err := LoadCatalog(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestFilterFailures_NoFilter(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "f1", Category: "database"},
			{ID: "f2", Category: "kubernetes"},
			{ID: "f3", Category: "compound"},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{})
	if len(result) != 3 {
		t.Errorf("FilterFailures(nil) = %d failures, want 3", len(result))
	}
}

func TestFilterFailures_ByCategory(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "db-1", Category: "database"},
			{ID: "db-2", Category: "database"},
			{ID: "k8s-1", Category: "kubernetes"},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{Categories: []string{"database"}})
	if len(result) != 2 {
		t.Errorf("FilterFailures([database]) = %d failures, want 2", len(result))
	}
	for _, f := range result {
		if f.Category != "database" {
			t.Errorf("got category %q, want %q", f.Category, "database")
		}
	}
}

func TestFilterFailures_ByID(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "db-1", Category: "database"},
			{ID: "db-2", Category: "database"},
			{ID: "k8s-1", Category: "kubernetes"},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{FailureIDs: []string{"db-2"}})
	if len(result) != 1 {
		t.Fatalf("FilterFailures([db-2]) = %d failures, want 1", len(result))
	}
	if result[0].ID != "db-2" {
		t.Errorf("got ID %q, want %q", result[0].ID, "db-2")
	}
}

func TestFilterFailures_ByCategoryAndID(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "db-1", Category: "database"},
			{ID: "db-2", Category: "database"},
			{ID: "k8s-1", Category: "kubernetes"},
		},
	}

	// ID filter takes precedence; category also matches.
	result := FilterFailures(catalog, &HarnessConfig{Categories: []string{"database"}, FailureIDs: []string{"k8s-1"}})
	if len(result) != 3 {
		t.Errorf("FilterFailures([database], [k8s-1]) = %d failures, want 3", len(result))
	}
}

func TestFilterFailures_External(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "sql-1", Category: "database", ExternalCompat: true},
			{ID: "docker-1", Category: "database", ExternalCompat: false},
			{ID: "sql-2", Category: "database", ExternalCompat: true},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{External: true})
	if len(result) != 2 {
		t.Errorf("FilterFailures(external=true) = %d, want 2", len(result))
	}
}

func TestFilterFailures_RealCatalog(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "catalog", "failures.yaml")
	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog error: %v", err)
	}

	// Filter by database category should return at least 16 failures.
	dbFailures := FilterFailures(catalog, &HarnessConfig{Categories: []string{"database"}})
	if len(dbFailures) < 16 {
		t.Errorf("database category count = %d, want >= 16", len(dbFailures))
	}

	// Filter by kubernetes category should return at least 7 failures.
	k8sFailures := FilterFailures(catalog, &HarnessConfig{Categories: []string{"kubernetes"}})
	if len(k8sFailures) < 7 {
		t.Errorf("kubernetes category count = %d, want >= 7", len(k8sFailures))
	}

	// Filter by host category should return at least 2 failures.
	hostFailures := FilterFailures(catalog, &HarnessConfig{Categories: []string{"host"}})
	if len(hostFailures) < 2 {
		t.Errorf("host category count = %d, want >= 2", len(hostFailures))
	}

	// Filter by compound category should return at least 2 failures.
	compoundFailures := FilterFailures(catalog, &HarnessConfig{Categories: []string{"compound"}})
	if len(compoundFailures) < 2 {
		t.Errorf("compound category count = %d, want >= 2", len(compoundFailures))
	}

	// External filter: only external_compat faults.
	extFailures := FilterFailures(catalog, &HarnessConfig{External: true})
	for _, f := range extFailures {
		if !f.ExternalCompat {
			t.Errorf("external filter returned non-compatible fault %q", f.ID)
		}
	}
}

func TestResolvePrompt(t *testing.T) {
	cfg := &HarnessConfig{
		ConnStr:        "host=db.example.com port=5432 dbname=prod",
		ReplicaConnStr: "host=replica.example.com port=5432 dbname=prod",
		KubeContext:    "gke_prod",
	}

	prompt := "Connect to {{connection_string}} and check replica at {{replica_connection_string}} in context {{kube_context}}"
	result := ResolvePrompt(prompt, cfg)

	expected := "Connect to host=db.example.com port=5432 dbname=prod and check replica at host=replica.example.com port=5432 dbname=prod in context gke_prod"
	if result != expected {
		t.Errorf("ResolvePrompt result =\n%s\nwant:\n%s", result, expected)
	}
}

func TestResolvePrompt_NoPlaceholders(t *testing.T) {
	cfg := &HarnessConfig{
		ConnStr: "host=db.example.com",
	}

	prompt := "Simple prompt with no placeholders"
	result := ResolvePrompt(prompt, cfg)

	if result != prompt {
		t.Errorf("ResolvePrompt changed text unexpectedly: %s", result)
	}
}

func TestTimeoutDuration_Valid(t *testing.T) {
	f := Failure{Timeout: "60s"}
	d := f.TimeoutDuration()
	if d != 60*time.Second {
		t.Errorf("TimeoutDuration(60s) = %v, want 60s", d)
	}
}

func TestTimeoutDuration_Minutes(t *testing.T) {
	f := Failure{Timeout: "2m"}
	d := f.TimeoutDuration()
	if d != 2*time.Minute {
		t.Errorf("TimeoutDuration(2m) = %v, want 2m", d)
	}
}

func TestTimeoutDuration_Empty(t *testing.T) {
	f := Failure{Timeout: ""}
	d := f.TimeoutDuration()
	if d != 60*time.Second {
		t.Errorf("TimeoutDuration('') = %v, want 60s default", d)
	}
}

func TestTimeoutDuration_Invalid(t *testing.T) {
	f := Failure{Timeout: "invalid"}
	d := f.TimeoutDuration()
	if d != 60*time.Second {
		t.Errorf("TimeoutDuration(invalid) = %v, want 60s default", d)
	}
}

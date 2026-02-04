package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

	// The real catalog has 17 failures: 9 database, 6 kubernetes, 2 compound.
	if len(catalog.Failures) != 17 {
		t.Errorf("Failures count = %d, want 17", len(catalog.Failures))
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

func TestLoadCatalog_MissingVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	data := `failures:
  - id: test-1
    name: Test failure
    category: test
`
	os.WriteFile(path, []byte(data), 0644)

	_, err := LoadCatalog(path)
	if err == nil {
		t.Fatal("expected error for missing version")
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

	result := FilterFailures(catalog, nil, nil)
	if len(result) != 3 {
		t.Errorf("FilterFailures(nil, nil) = %d failures, want 3", len(result))
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

	result := FilterFailures(catalog, []string{"database"}, nil)
	if len(result) != 2 {
		t.Errorf("FilterFailures([database], nil) = %d failures, want 2", len(result))
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

	result := FilterFailures(catalog, nil, []string{"db-2"})
	if len(result) != 1 {
		t.Fatalf("FilterFailures(nil, [db-2]) = %d failures, want 1", len(result))
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
	result := FilterFailures(catalog, []string{"database"}, []string{"k8s-1"})
	if len(result) != 3 {
		t.Errorf("FilterFailures([database], [k8s-1]) = %d failures, want 3", len(result))
	}
}

func TestFilterFailures_RealCatalog(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "catalog", "failures.yaml")
	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog error: %v", err)
	}

	// Filter by database category should return 9 failures.
	dbFailures := FilterFailures(catalog, []string{"database"}, nil)
	if len(dbFailures) != 9 {
		t.Errorf("database category count = %d, want 9", len(dbFailures))
	}

	// Filter by kubernetes category should return 6 failures.
	k8sFailures := FilterFailures(catalog, []string{"kubernetes"}, nil)
	if len(k8sFailures) != 6 {
		t.Errorf("kubernetes category count = %d, want 6", len(k8sFailures))
	}

	// Filter by compound category should return 2 failures.
	compoundFailures := FilterFailures(catalog, []string{"compound"}, nil)
	if len(compoundFailures) != 2 {
		t.Errorf("compound category count = %d, want 2", len(compoundFailures))
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

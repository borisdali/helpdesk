package faultlib

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadCatalog_Valid(t *testing.T) {
	// Find the catalog relative to this test file.
	catalogPath := findCatalog()
	if catalogPath == "" {
		t.Skip("Could not find catalog/failures.yaml")
	}

	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog error: %v", err)
	}

	if catalog.Version != "1" {
		t.Errorf("Version = %q, want %q", catalog.Version, "1")
	}

	if len(catalog.Failures) != 19 {
		t.Errorf("Failures count = %d, want 19", len(catalog.Failures))
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
	os.WriteFile(path, []byte("failures:\n  - id: test-1\n"), 0644)

	_, err := LoadCatalog(path)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestFilterFailures_NoFilter(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "f1", Category: "database"},
			{ID: "f2", Category: "kubernetes"},
		},
	}

	result := FilterFailures(catalog, nil, nil)
	if len(result) != 2 {
		t.Errorf("FilterFailures(nil, nil) = %d, want 2", len(result))
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
		t.Errorf("FilterFailures([database], nil) = %d, want 2", len(result))
	}
}

func TestFilterFailures_ByID(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "db-1", Category: "database"},
			{ID: "db-2", Category: "database"},
		},
	}

	result := FilterFailures(catalog, nil, []string{"db-2"})
	if len(result) != 1 {
		t.Fatalf("FilterFailures(nil, [db-2]) = %d, want 1", len(result))
	}
	if result[0].ID != "db-2" {
		t.Errorf("got ID %q, want %q", result[0].ID, "db-2")
	}
}

func TestResolvePrompt(t *testing.T) {
	cfg := &HarnessConfig{
		ConnStr:        "host=db.example.com port=5432",
		ReplicaConnStr: "host=replica.example.com",
		KubeContext:    "gke_prod",
	}

	prompt := "Connect to {{connection_string}} in context {{kube_context}}"
	result := ResolvePrompt(prompt, cfg)

	expected := "Connect to host=db.example.com port=5432 in context gke_prod"
	if result != expected {
		t.Errorf("ResolvePrompt = %q, want %q", result, expected)
	}
}

func TestTimeoutDuration(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{"valid 60s", "60s", 60 * time.Second},
		{"valid 2m", "2m", 2 * time.Minute},
		{"empty", "", 60 * time.Second},
		{"invalid", "invalid", 60 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Failure{Timeout: tt.timeout}
			got := f.TimeoutDuration()
			if got != tt.want {
				t.Errorf("TimeoutDuration(%q) = %v, want %v", tt.timeout, got, tt.want)
			}
		})
	}
}

func TestEvaluate_AllPass(t *testing.T) {
	f := Failure{
		ID:       "test-1",
		Name:     "Test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:     []string{"check_connection"},
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"refused"}},
			ExpectedDiagnosis: DiagnosisSpec{Category: "connection_refused"},
		},
	}

	response := "The connection was refused. Cannot connect to server."
	result := Evaluate(f, response)

	if !result.Passed {
		t.Errorf("Evaluate should pass, got Passed=%v, Score=%.2f", result.Passed, result.Score)
	}
	if !result.KeywordPass {
		t.Error("KeywordPass should be true")
	}
}

func TestEvaluate_KeywordFail(t *testing.T) {
	f := Failure{
		ID:       "test-2",
		Name:     "Test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"max_connections"}},
		},
	}

	response := "The database is running normally."
	result := Evaluate(f, response)

	if result.KeywordPass {
		t.Error("KeywordPass should be false")
	}
	if result.Passed {
		t.Error("Evaluate should fail")
	}
}

func TestSplitCategory(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"connection_exhaustion", []string{"connection", "exhaustion"}},
		{"pod-crash-loop", []string{"pod", "crash", "loop"}},
		{"single", []string{"single"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SplitCategory(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitCategory(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEvaluate_ToolOrdering_Pass(t *testing.T) {
	// get_session_info evidence ("session", "pid") precedes terminate_connection
	// evidence ("terminated") in the response text.
	f := Failure{
		ID: "ordering-pass",
		Evaluation: EvalSpec{
			ExpectedToolOrder: [][]string{
				{"get_session_info", "terminate_connection"},
			},
		},
	}
	response := "Inspecting session: pid 1234 is active and holds no open transaction. Terminated the backend."
	result := Evaluate(f, response)
	if !result.OrderingPass {
		t.Errorf("OrderingPass = false; expected session_info evidence to precede terminate evidence in %q", response)
	}
}

func TestEvaluate_ToolOrdering_Fail(t *testing.T) {
	// terminate_connection evidence appears before get_session_info evidence.
	f := Failure{
		ID: "ordering-fail",
		Evaluation: EvalSpec{
			ExpectedToolOrder: [][]string{
				{"get_session_info", "terminate_connection"},
			},
		},
	}
	response := "Terminated backend. After that, checked session state: pid 1234 was active."
	result := Evaluate(f, response)
	if result.OrderingPass {
		t.Errorf("OrderingPass = true; expected false when terminate evidence precedes session_info evidence in %q", response)
	}
}

func TestEvaluate_ToolOrdering_MissingTool(t *testing.T) {
	// terminate_connection evidence is absent — ordering cannot be confirmed.
	f := Failure{
		ID: "ordering-missing",
		Evaluation: EvalSpec{
			ExpectedToolOrder: [][]string{
				{"get_session_info", "terminate_connection"},
			},
		},
	}
	response := "Inspecting session: pid 1234 is active."
	result := Evaluate(f, response)
	if result.OrderingPass {
		t.Errorf("OrderingPass = true; expected false when one tool has no evidence in %q", response)
	}
}

func TestEvaluate_ToolOrdering_EmptyOrder_AlwaysPasses(t *testing.T) {
	// No ExpectedToolOrder → OrderingPass is always true (backwards compatible).
	f := Failure{
		ID: "ordering-none",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused"}},
		},
	}
	response := "Connection refused."
	result := Evaluate(f, response)
	if !result.OrderingPass {
		t.Error("OrderingPass should be true when ExpectedToolOrder is empty")
	}
}

func TestEvaluate_OrderingGatesPassed(t *testing.T) {
	// High keyword + tool score but wrong ordering → Passed = false.
	f := Failure{
		ID:       "ordering-gates-passed",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"terminated"}},
			ExpectedToolOrder: [][]string{{"get_session_info", "terminate_connection"}},
		},
	}
	// "terminated" appears first, "session" / "pid" appears after.
	response := "Terminated the backend. Checked session info: pid 1234 was active."
	result := Evaluate(f, response)
	if result.KeywordPass && result.Passed {
		t.Errorf("Passed should be false when ordering fails even if keyword passes; Score=%.2f, KeywordPass=%v, OrderingPass=%v",
			result.Score, result.KeywordPass, result.OrderingPass)
	}
}

// findCatalog tries to locate the failures.yaml file.
func findCatalog() string {
	paths := []string{
		"../catalog/failures.yaml",
		"../../catalog/failures.yaml",
		"testing/catalog/failures.yaml",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

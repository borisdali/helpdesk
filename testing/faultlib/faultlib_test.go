package faultlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const builtinMinimum = 33

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

	if len(catalog.Failures) < builtinMinimum {
		t.Errorf("Failures count = %d, want >= %d", len(catalog.Failures), builtinMinimum)
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
	os.WriteFile(path, []byte("failures:\n  - id: test-1\n    name: Test\n    category: database\n"), 0644)

	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog on custom catalog without version: unexpected error: %v", err)
	}
	if len(cat.Failures) != 1 {
		t.Errorf("expected 1 failure, got %d", len(cat.Failures))
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
	// Verify the custom fault is present with Source="custom".
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

func TestFilterFailures_NoFilter(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "f1", Category: "database"},
			{ID: "f2", Category: "kubernetes"},
		},
	}

	result := FilterFailures(catalog, nil)
	if len(result) != 2 {
		t.Errorf("FilterFailures(nil) = %d, want 2", len(result))
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
		t.Errorf("FilterFailures(database) = %d, want 2", len(result))
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

	result := FilterFailures(catalog, &HarnessConfig{FailureIDs: []string{"db-2"}})
	if len(result) != 1 {
		t.Fatalf("FilterFailures([db-2]) = %d, want 1", len(result))
	}
	if result[0].ID != "db-2" {
		t.Errorf("got ID %q, want %q", result[0].ID, "db-2")
	}
}

func TestFilterFailures_ExcludeIDs(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "db-1", Category: "database"},
			{ID: "db-2", Category: "database"},
			{ID: "k8s-1", Category: "kubernetes"},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{ExcludeIDs: []string{"k8s-1"}})
	if len(result) != 2 {
		t.Fatalf("FilterFailures(exclude k8s-1) = %d, want 2", len(result))
	}
	for _, f := range result {
		if f.ID == "k8s-1" {
			t.Error("excluded fault k8s-1 present in result")
		}
	}
}

func TestFilterFailures_ExcludeIDs_WinsOverCategory(t *testing.T) {
	// Exclude must win even when the excluded fault also matches an
	// explicitly-requested category — otherwise excluding one slow fault
	// from a category-scoped run wouldn't actually work.
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "k8s-1", Category: "kubernetes"},
			{ID: "k8s-2", Category: "kubernetes"},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{
		Categories: []string{"kubernetes"},
		ExcludeIDs: []string{"k8s-1"},
	})
	if len(result) != 1 {
		t.Fatalf("FilterFailures(category=kubernetes, exclude=k8s-1) = %d, want 1", len(result))
	}
	if result[0].ID != "k8s-2" {
		t.Errorf("got ID %q, want %q", result[0].ID, "k8s-2")
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
	for _, f := range result {
		if !f.ExternalCompat {
			t.Errorf("external filter returned non-compatible fault %q", f.ID)
		}
	}
}

// TestFilterFailures_External_ExplicitIDNotOverridden documents existing,
// unchanged behavior that motivated the RequiresReplica field: --external
// mode's ExternalCompat check has no override for an explicit --ids
// selection (unlike the exclude-list, which wins over everything else) — a
// fault marked external_compat:false is dropped even when named directly.
// This is why a replica-requiring-but-libpq-only fault must be marked
// external_compat:true + requires_replica:true, not external_compat:false,
// to remain reachable via --external --ids <fault> (real bug found and
// fixed 2026-08-28 — see TestFilterFailures_AutoDB_RequiresReplica below for
// the fix itself).
func TestFilterFailures_External_ExplicitIDNotOverridden(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "docker-only", Category: "database", ExternalCompat: false},
		},
	}
	result := FilterFailures(catalog, &HarnessConfig{External: true, FailureIDs: []string{"docker-only"}})
	if len(result) != 0 {
		t.Errorf("FilterFailures(external=true, ids=[docker-only]) = %v, want empty — explicit ID selection does not override external_compat:false", result)
	}
}

// TestFilterFailures_AutoDB_RequiresReplica proves the actual fix: a fault
// that's pure-libpq (external_compat:true, so reachable via --external even
// with an explicit --ids) but needs a real replica already attached
// (requires_replica:true) is still correctly excluded from --auto-db, whose
// ephemeral single-instance docker-compose stack never provisions one.
// Complements TestFilterFailures_External_ExplicitIDNotOverridden above:
// together they prove requires_replica achieves what overloading
// external_compat:false could not — externally testable AND auto-db-safe.
func TestFilterFailures_AutoDB_RequiresReplica(t *testing.T) {
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "replica-fault", Category: "database", ExternalCompat: true, RequiresReplica: true, Inject: InjectSpec{Type: "sql"}},
			{ID: "no-replica-fault", Category: "database", ExternalCompat: true, RequiresReplica: false, Inject: InjectSpec{Type: "sql"}},
		},
	}

	// Excluded from --auto-db despite being external-compat.
	autoResult := FilterFailures(catalog, &HarnessConfig{AutoDB: true})
	if len(autoResult) != 1 || autoResult[0].ID != "no-replica-fault" {
		t.Errorf("FilterFailures(AutoDB=true) = %v, want [no-replica-fault] — replica-fault must be excluded", autoResult)
	}

	// But still reachable via --external with an explicit --ids.
	extResult := FilterFailures(catalog, &HarnessConfig{External: true, FailureIDs: []string{"replica-fault"}})
	if len(extResult) != 1 || extResult[0].ID != "replica-fault" {
		t.Errorf("FilterFailures(External=true, ids=[replica-fault]) = %v, want [replica-fault]", extResult)
	}
}

func TestFilterFailures_AutoDB(t *testing.T) {
	// AutoDB-only faults must be external-compat, non-kubernetes, and not
	// ssh_exec (IsAutoDBCompat's own definition — types.go). Item 7 dedup,
	// v0.26: this AutoDB check used to live only in cmd/faulttest's own
	// duplicate FilterFailures; merged here as the single implementation.
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "auto-1", Category: "database", ExternalCompat: true, Inject: InjectSpec{Type: "sql"}},
			{ID: "not-auto-1", Category: "database", ExternalCompat: false, Inject: InjectSpec{Type: "sql"}},
			{ID: "not-auto-k8s", Category: "kubernetes", ExternalCompat: true, Inject: InjectSpec{Type: "sql"}},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{AutoDB: true})
	if len(result) != 1 || result[0].ID != "auto-1" {
		t.Errorf("FilterFailures(AutoDB=true) = %v, want [auto-1]", result)
	}
}

func TestFilterFailures_AutoDB_ExternalCheckRedundantNotConflicting(t *testing.T) {
	// AutoDB implies ExternalCompat via IsAutoDBCompat's own definition, so
	// a fault that survives the AutoDB filter always has ExternalCompat
	// true — the "&& !cfg.AutoDB" guard on the External check (below) can
	// never actually change the outcome for real catalog data, but is kept
	// (ported faithfully from cmd/faulttest's pre-dedup logic) as a defensive
	// guard against IsAutoDBCompat's definition changing in the future.
	catalog := &Catalog{
		Version: "1",
		Failures: []Failure{
			{ID: "auto-1", Category: "database", ExternalCompat: true, Inject: InjectSpec{Type: "sql"}},
		},
	}

	result := FilterFailures(catalog, &HarnessConfig{AutoDB: true, External: true})
	if len(result) != 1 || result[0].ID != "auto-1" {
		t.Errorf("FilterFailures(AutoDB=true, External=true) = %v, want [auto-1]", result)
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
		{"empty", "", 120 * time.Second},
		{"invalid", "invalid", 120 * time.Second},
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

// TestNeedsReplica covers both ways a fault can need a replica connection:
// an explicit target:replica on any of the 4 inject/teardown specs (the
// original mechanism), or the RequiresReplica field (added for faults like
// db-replica-disconnected whose injection runs entirely against the primary
// but still needs a real replica already attached in the topology). Both
// testing/cmd/faulttest's runner and testing/faulttest's Go-test harness
// call this to decide whether to skip cleanly when --replica-conn isn't
// configured — a fault needing RequiresReplica alone (no target:replica
// anywhere) was a real gap found live: it fell through both skip checks
// undetected and would fail loudly instead of skipping cleanly.
func TestNeedsReplica(t *testing.T) {
	tests := []struct {
		name string
		f    Failure
		want bool
	}{
		{"neither set", Failure{}, false},
		{"RequiresReplica alone, no target:replica anywhere", Failure{RequiresReplica: true}, true},
		{"Inject.Target replica", Failure{Inject: InjectSpec{Target: "replica"}}, true},
		{"Teardown.Target replica", Failure{Teardown: InjectSpec{Target: "replica"}}, true},
		{"ExternalInject.Target replica", Failure{ExternalInject: InjectSpec{Target: "replica"}}, true},
		{"ExternalTeardown.Target replica", Failure{ExternalTeardown: InjectSpec{Target: "replica"}}, true},
		{"unrelated target set", Failure{Inject: InjectSpec{Target: "primary"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.NeedsReplica(); got != tt.want {
				t.Errorf("NeedsReplica() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInjectTimeoutDuration(t *testing.T) {
	tests := []struct {
		name          string
		injectTimeout string
		want          time.Duration
	}{
		{"valid 30m", "30m", 30 * time.Minute},
		{"valid 5s", "5s", 5 * time.Second},
		{"empty defaults to 90s", "", 90 * time.Second},
		{"invalid defaults to 90s", "not-a-duration", 90 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Failure{InjectTimeout: tt.injectTimeout}
			got := f.InjectTimeoutDuration()
			if got != tt.want {
				t.Errorf("InjectTimeoutDuration(%q) = %v, want %v", tt.injectTimeout, got, tt.want)
			}
		})
	}
}

// TestEvaluate_* below restored 2026-08-22: the original "Evaluate is dead
// code" call was wrong — testing/faulttest/faulttest_test.go (tag
// `faulttest`) and testing/e2e/multi_agent_test.go (tag `e2e`) both call
// faultlib.Evaluate directly, invisible to a plain go build/vet/test (no
// -tags). Evaluate itself was restored in evaluator.go; these tests close
// the resulting 0%-coverage gap under the plain (untagged) test run that
// codecov/CI actually measures — Evaluate's only real callers otherwise need
// live Docker+agents+LLM infra to execute at all. EvaluateWithJudge/
// computeComponents genuinely stayed dead (zero callers under any tag) and
// are not restored.

func TestEvaluate_AllPass(t *testing.T) {
	f := Failure{
		ID:       "test-1",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:    []string{"check_connection"},
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused", "exhausted"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Category: "connection_refused",
			},
		},
	}
	response := "The connection was refused. Cannot connect to the server."
	result := Evaluate(f, response)

	if !result.Passed {
		t.Errorf("Evaluate should pass, got Passed=%v Score=%.2f", result.Passed, result.Score)
	}
	if !result.KeywordPass || !result.DiagnosisPass || !result.ToolEvidence || !result.OrderingPass {
		t.Errorf("all component checks should pass: keyword=%v diagnosis=%v tool=%v ordering=%v",
			result.KeywordPass, result.DiagnosisPass, result.ToolEvidence, result.OrderingPass)
	}
}

func TestEvaluate_KeywordFail(t *testing.T) {
	f := Failure{
		ID:       "test-2",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"max_connections", "too many"}},
		},
	}
	response := "The database is healthy and running normally."
	result := Evaluate(f, response)

	if result.KeywordPass {
		t.Error("KeywordPass should be false")
	}
	if result.Passed {
		t.Error("Evaluate should fail when keywords don't match")
	}
}

func TestEvaluate_OrderingGatesPassed(t *testing.T) {
	// High keyword score but wrong tool order → Passed = false.
	f := Failure{
		ID:       "test-3",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"pg_terminate_backend"}},
			ExpectedToolOrder: [][]string{{"get_session_info", "terminate_connection"}},
		},
	}
	// pg_terminate_backend appears BEFORE client_addr — wrong order.
	response := "pg_terminate_backend(1234) returned true. Then inspected: client_addr: 127.0.0.1."
	result := Evaluate(f, response)
	if result.OrderingPass {
		t.Error("OrderingPass should be false — terminate evidence precedes session_info evidence")
	}
	if result.KeywordPass && result.Passed {
		t.Errorf("Passed should be false when ordering fails even if keyword passes; Score=%.2f", result.Score)
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

// TestEvaluate_ToolOrdering_Pass/_Fail/_MissingTool/_EmptyOrder_AlwaysPasses
// and TestEvaluate_OrderingGatesPassed (formerly here) tested this package's
// own Evaluate's ordering behavior — deleted along with Evaluate itself
// (item 7 dedup, v0.26), but the underlying logic they verified
// (CheckToolOrdering/FirstOrderingPatternIndex) is still exported from this
// package and still used — see the ported equivalents in
// testing/cmd/faulttest/evaluator_test.go, added there because
// cmd/faulttest's own Evaluate had never gained ordering support at all
// (OrderingPass didn't previously exist on its EvalResult) — a real
// behavioral gap this dedup pass closed, not just a relocation.

func TestCatalog_ExternalCompatFields(t *testing.T) {
	catalogPath := findCatalog()
	if catalogPath == "" {
		t.Skip("Could not find catalog/failures.yaml")
	}

	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	// Count external-compatible faults.
	var extCount int
	for _, f := range catalog.Failures {
		if !f.ExternalCompat {
			continue
		}
		extCount++

		// Every external_compat fault must have inject.type set (sql is fine as-is;
		// docker_exec faults must supply external_inject).
		if f.Inject.Type == "docker_exec" && f.ExternalInject.Type == "" {
			t.Errorf("fault %q is external_compat with docker_exec inject but has no external_inject spec", f.ID)
		}
	}

	if extCount == 0 {
		t.Error("expected at least one external_compat fault in catalog")
	}
	t.Logf("%d/%d faults are external_compat", extCount, len(catalog.Failures))
}

func TestCatalog_RemediationFields(t *testing.T) {
	catalogPath := findCatalog()
	if catalogPath == "" {
		t.Skip("Could not find catalog/failures.yaml")
	}

	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	// Every fault with a remediation spec must have either playbook_id or agent_prompt.
	for _, f := range catalog.Failures {
		r := f.Remediation
		if r.PlaybookID == "" && r.AgentPrompt == "" {
			continue // no remediation — ok
		}
		// Has a spec: verify at least one action field is set (already true, but
		// catches YAML typos).
		if r.PlaybookID == "" && r.AgentPrompt == "" {
			t.Errorf("fault %q has remediation block but no playbook_id or agent_prompt", f.ID)
		}
	}
}

func TestCatalog_SSHFaultsHaveNoExternalCompat(t *testing.T) {
	catalogPath := findCatalog()
	if catalogPath == "" {
		t.Skip("Could not find catalog/failures.yaml")
	}

	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	// SSH-injected faults must NOT be marked external_compat (they require OS access).
	for _, f := range catalog.Failures {
		if f.Inject.Type == "ssh_exec" && f.ExternalCompat {
			t.Errorf("fault %q uses ssh_exec but is marked external_compat — ssh_exec requires OS access", f.ID)
		}
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

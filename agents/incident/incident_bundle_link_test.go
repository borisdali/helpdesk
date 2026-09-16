package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

// readManifestTraceID extracts manifest.json from a bundle tarball and
// returns its trace_id field.
func readManifestTraceID(t *testing.T, bundlePath string) string {
	t.Helper()
	f, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if strings.HasSuffix(hdr.Name, "manifest.json") {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read manifest.json: %v", err)
			}
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("decode manifest.json: %v", err)
			}
			return m.TraceID
		}
	}
	t.Fatal("manifest.json not found in bundle")
	return ""
}

// --- doPatchIncidentBundlePath ---

func TestDoPatchIncidentBundlePath_Success(t *testing.T) {
	var gotMethod, gotBody, gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		r.Body.Read(b) //nolint:errcheck
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := doPatchIncidentBundlePath(context.Background(), srv.URL, "secret-key", "inc_abc", "/data/incidents/inc_abc.tar.gz")
	if err != nil {
		t.Fatalf("doPatchIncidentBundlePath: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/v1/incidents/inc_abc" {
		t.Errorf("path = %q, want /v1/incidents/inc_abc", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["bundle_path"] != "/data/incidents/inc_abc.tar.gz" {
		t.Errorf("bundle_path = %q, want /data/incidents/inc_abc.tar.gz", body["bundle_path"])
	}
}

func TestDoPatchIncidentBundlePath_EmptyAuditURL(t *testing.T) {
	err := doPatchIncidentBundlePath(context.Background(), "", "", "inc_abc", "/data/x.tar.gz")
	if err == nil {
		t.Error("expected error for empty audit URL, got nil")
	}
}

func TestDoPatchIncidentBundlePath_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("incident not found")) //nolint:errcheck
	}))
	defer srv.Close()

	err := doPatchIncidentBundlePath(context.Background(), srv.URL, "", "inc_missing", "/data/x.tar.gz")
	if err == nil {
		t.Fatal("expected error for a 404 response, got nil")
	}
}

// --- createIncidentBundleImpl: IncidentID-gated PATCH vs. legacy appendToIndex ---

// withMockedLayers replaces runCommand for the duration of the test so
// collectOSLayer/collectStorageLayer (always-on layers) don't shell out to
// real system commands.
func withMockedLayers(t *testing.T) {
	t.Helper()
	origCommand := runCommand
	t.Cleanup(func() { runCommand = origCommand })
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "mock output for " + name, nil
	}
}

func TestCreateIncidentBundleImpl_WithIncidentID_PatchesAuditd_NotIndexFile(t *testing.T) {
	withMockedLayers(t)
	outputDir := t.TempDir()
	t.Setenv("HELPDESK_INCIDENT_DIR", outputDir)

	var patchedPath string
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		patchedPath = body["bundle_path"]
		w.WriteHeader(http.StatusNoContent)
	}))
	defer auditSrv.Close()
	t.Setenv("HELPDESK_AUDIT_URL", auditSrv.URL)

	result, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{
		IncidentID: "inc_linked01",
	})
	if err != nil {
		t.Fatalf("createIncidentBundleImpl: %v", err)
	}
	if patchedPath == "" {
		t.Fatal("auditd never received a PATCH — bundle_path was not linked to the incidents table")
	}
	if patchedPath != result.BundlePath {
		t.Errorf("patched bundle_path = %q, want %q (result.BundlePath)", patchedPath, result.BundlePath)
	}

	if _, err := os.Stat(filepath.Join(outputDir, "incidents.json")); !os.IsNotExist(err) {
		t.Error("incidents.json should NOT be written when IncidentID links to the real incidents table")
	}
}

// TestCreateIncidentBundleImpl_WithIncidentID_AuditURLMisconfigured_BundleStillCreated
// documents a real, accepted edge case: when a caller supplies IncidentID
// (implying auditd should be configured) but HELPDESK_AUDIT_URL is actually
// unset/misconfigured, the bundle is still written to disk, but it lands in
// NEITHER the incidents table (PATCH fails) NOR the legacy incidents.json
// index (skipped, since IncidentID was supplied) — a genuinely untracked
// bundle. Best-effort, matching this codebase's established convention
// elsewhere (a failed downstream write logs and moves on rather than falling
// back to a different write path) — not a crash, not silently discarding the
// tarball itself, just its index entry. Documented here so this is a known,
// deliberate tradeoff if it's ever noticed in the field, not a surprise.
func TestCreateIncidentBundleImpl_WithIncidentID_AuditURLMisconfigured_BundleStillCreated(t *testing.T) {
	withMockedLayers(t)
	outputDir := t.TempDir()
	t.Setenv("HELPDESK_INCIDENT_DIR", outputDir)
	t.Setenv("HELPDESK_AUDIT_URL", "") // misconfigured despite IncidentID being supplied

	result, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{
		IncidentID: "inc_orphaned01",
	})
	if err != nil {
		t.Fatalf("createIncidentBundleImpl should not fail outright: %v", err)
	}
	if _, err := os.Stat(result.BundlePath); err != nil {
		t.Errorf("bundle tarball should still be written to disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "incidents.json")); !os.IsNotExist(err) {
		t.Error("incidents.json should still be skipped when IncidentID is supplied, even if the PATCH failed")
	}
}

func TestCreateIncidentBundleImpl_WithoutIncidentID_WritesIndexFile(t *testing.T) {
	withMockedLayers(t)
	outputDir := t.TempDir()
	t.Setenv("HELPDESK_INCIDENT_DIR", outputDir)
	t.Setenv("HELPDESK_AUDIT_URL", "") // no auditd configured — legacy caller

	_, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{
		Description: "legacy manual bundle",
	})
	if err != nil {
		t.Fatalf("createIncidentBundleImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outputDir, "incidents.json"))
	if err != nil {
		t.Fatalf("incidents.json should exist for a legacy (no IncidentID) call: %v", err)
	}
	var entries []IndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode incidents.json: %v", err)
	}
	if len(entries) != 1 || entries[0].Description != "legacy manual bundle" {
		t.Errorf("entries = %+v, want one entry with the legacy description", entries)
	}
}

func TestCreateIncidentBundleImpl_TraceIDFromCurrentTraceStore(t *testing.T) {
	withMockedLayers(t)
	outputDir := t.TempDir()
	t.Setenv("HELPDESK_INCIDENT_DIR", outputDir)
	t.Setenv("HELPDESK_AUDIT_URL", "")

	origStore := currentTraceStore
	t.Cleanup(func() { currentTraceStore = origStore })
	currentTraceStore = &audit.CurrentTraceStore{}
	currentTraceStore.Set("tr_manifest_test")

	result, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{})
	if err != nil {
		t.Fatalf("createIncidentBundleImpl: %v", err)
	}

	manifestTraceID := readManifestTraceID(t, result.BundlePath)
	if manifestTraceID != "tr_manifest_test" {
		t.Errorf("manifest trace_id = %q, want tr_manifest_test", manifestTraceID)
	}
}

// TestCreateIncidentBundleImpl_SeriesIDThreadsToFromTraceRequest proves
// args.SeriesID actually reaches the from-trace HTTP request body end-to-end
// through createIncidentBundleImpl -> requestPlaybookDraft ->
// doPlaybookDraftRequest -> agentutil.RequestPlaybookDraft, not just that the
// shared helper accepts a seriesID parameter in isolation.
func TestCreateIncidentBundleImpl_SeriesIDThreadsToFromTraceRequest(t *testing.T) {
	withMockedLayers(t)
	outputDir := t.TempDir()
	t.Setenv("HELPDESK_INCIDENT_DIR", outputDir)
	t.Setenv("HELPDESK_AUDIT_URL", "")

	var gotBody string
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b) //nolint:errcheck
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"playbook_id": "pb_improved01"}) //nolint:errcheck
	}))
	defer gatewaySrv.Close()
	t.Setenv("HELPDESK_GATEWAY_URL", gatewaySrv.URL)

	result, err := createIncidentBundleImpl(context.Background(), CreateIncidentBundleArgs{
		Outcome:  "resolved",
		SeriesID: "pbs_vacuum_triage",
	})
	if err != nil {
		t.Fatalf("createIncidentBundleImpl: %v", err)
	}
	if result.PlaybookID != "pb_improved01" {
		t.Errorf("result.PlaybookID = %q, want pb_improved01", result.PlaybookID)
	}

	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("from-trace request body is not JSON: %v", err)
	}
	if body["series_id"] != "pbs_vacuum_triage" {
		t.Errorf("series_id = %q, want pbs_vacuum_triage", body["series_id"])
	}
}

// --- NewIncidentDirectRegistry ---

func TestNewIncidentDirectRegistry_CreateIncidentBundle_Success(t *testing.T) {
	withMockedLayers(t)
	outputDir := t.TempDir()
	t.Setenv("HELPDESK_INCIDENT_DIR", outputDir)
	t.Setenv("HELPDESK_AUDIT_URL", "")

	reg := NewIncidentDirectRegistry()
	fn, ok := reg.Get("create_incident_bundle")
	if !ok {
		t.Fatal("create_incident_bundle not registered")
	}

	out, err := fn(context.Background(), map[string]any{"description": "direct-tool test"})
	if err != nil {
		t.Fatalf("direct tool call: %v", err)
	}
	var result IncidentBundleResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v — raw output: %s", err, out)
	}
	if result.BundlePath == "" {
		t.Error("BundlePath should be set")
	}
}

func TestNewIncidentDirectRegistry_UnknownTool(t *testing.T) {
	reg := NewIncidentDirectRegistry()
	if _, ok := reg.Get("not_a_real_tool"); ok {
		t.Error("Get should return false for an unregistered tool")
	}
}

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddFileToTar(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	data := []byte("hello world")
	if err := addFileToTar(tw, "test/file.txt", data); err != nil {
		t.Fatalf("addFileToTar error: %v", err)
	}
	tw.Close()

	// Read back and verify.
	tr := tar.NewReader(&buf)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next error: %v", err)
	}
	if hdr.Name != "test/file.txt" {
		t.Errorf("Name = %q, want %q", hdr.Name, "test/file.txt")
	}
	if hdr.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", hdr.Size, len(data))
	}
	if hdr.Mode != 0644 {
		t.Errorf("Mode = %o, want %o", hdr.Mode, 0644)
	}

	content, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("content = %q, want %q", string(content), "hello world")
	}
}

func TestAssembleTarball(t *testing.T) {
	tmpDir := t.TempDir()

	manifest := Manifest{
		IncidentID:  "INC-001",
		InfraKey:    "test-infra",
		Description: "test incident",
		Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Layers:      []string{"db-check", "k8s-pods"},
	}

	layers := map[string]map[string]string{
		"db-check": {
			"connection.txt": "Connection OK",
		},
		"k8s-pods": {
			"pods.txt": "NAME       READY\nnginx      1/1",
		},
	}

	outPath, err := assembleTarball(manifest, layers, tmpDir)
	if err != nil {
		t.Fatalf("assembleTarball error: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file missing: %v", err)
	}

	// Verify filename pattern.
	base := filepath.Base(outPath)
	if !strings.HasPrefix(base, "incident-INC-001-") {
		t.Errorf("filename = %q, want prefix 'incident-INC-001-'", base)
	}
	if !strings.HasSuffix(base, ".tar.gz") {
		t.Errorf("filename = %q, want .tar.gz suffix", base)
	}

	// Extract and verify contents.
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader error: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	files := make(map[string]string)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next error: %v", err)
		}
		data, _ := io.ReadAll(tr)
		files[hdr.Name] = string(data)
	}

	// Check manifest.json exists.
	var manifestFound bool
	for name := range files {
		if strings.HasSuffix(name, "manifest.json") {
			manifestFound = true
			if !strings.Contains(files[name], "INC-001") {
				t.Error("manifest.json missing incident ID")
			}
		}
	}
	if !manifestFound {
		t.Error("manifest.json not found in tarball")
	}

	// Check layer files exist.
	var dbCheckFound, k8sPodsFound bool
	for name := range files {
		if strings.Contains(name, "db-check/connection.txt") {
			dbCheckFound = true
		}
		if strings.Contains(name, "k8s-pods/pods.txt") {
			k8sPodsFound = true
		}
	}
	if !dbCheckFound {
		t.Error("db-check/connection.txt not found in tarball")
	}
	if !k8sPodsFound {
		t.Error("k8s-pods/pods.txt not found in tarball")
	}
}

func TestAssembleTarball_WithErrors(t *testing.T) {
	tmpDir := t.TempDir()

	manifest := Manifest{
		IncidentID:  "INC-002",
		InfraKey:    "test",
		Description: "error test",
		Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Layers:      []string{"db-check"},
		Errors:      []string{"psql timed out", "kubectl not found"},
	}

	layers := map[string]map[string]string{
		"db-check": {"output.txt": "partial output"},
	}

	outPath, err := assembleTarball(manifest, layers, tmpDir)
	if err != nil {
		t.Fatalf("assembleTarball error: %v", err)
	}

	// Extract and check errors.txt.
	f, _ := os.Open(outPath)
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	defer gr.Close()
	tr := tar.NewReader(gr)

	var errorsFound bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next error: %v", err)
		}
		if strings.HasSuffix(hdr.Name, "errors.txt") {
			data, _ := io.ReadAll(tr)
			content := string(data)
			errorsFound = true
			if !strings.Contains(content, "psql timed out") {
				t.Error("errors.txt missing first error")
			}
			if !strings.Contains(content, "kubectl not found") {
				t.Error("errors.txt missing second error")
			}
		}
	}
	if !errorsFound {
		t.Error("errors.txt not found in tarball")
	}
}

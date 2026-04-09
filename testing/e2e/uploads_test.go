//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestUploads_CRUDLifecycle exercises the full upload pipeline through the
// gateway: multipart POST → metadata GET → content GET.
//
// This test is particularly valuable because the gateway's upload handler uses
// a custom proxy (not proxyToAuditd) to correctly forward the multipart
// Content-Type header including the boundary parameter. Any regression there
// would cause a 400 from auditd.
func TestUploads_CRUDLifecycle(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logContent := fmt.Sprintf(
		"2024-01-15 10:00:00 UTC [1234]: LOG:  database system is ready\n"+
			"2024-01-15 10:00:01 UTC [1234]: FATAL: could not open file \"pg_hba.conf\"\n"+
			"e2e-upload-test-marker-%d\n",
		time.Now().UnixNano(),
	)
	filename := fmt.Sprintf("postgresql-e2e-%d.log", time.Now().UnixNano())

	// Create.
	created, err := client.UploadCreate(ctx, filename, logContent)
	if err != nil {
		t.Fatalf("UploadCreate: %v", err)
	}
	uploadID, _ := created["upload_id"].(string)
	if !strings.HasPrefix(uploadID, "ul_") {
		t.Errorf("upload_id = %q, want ul_ prefix", uploadID)
	}
	if got, _ := created["filename"].(string); got != filename {
		t.Errorf("filename = %q, want %q", got, filename)
	}
	if size, _ := created["size"].(float64); int(size) != len(logContent) {
		t.Errorf("size = %v, want %d", size, len(logContent))
	}
	t.Logf("uploaded: id=%s filename=%s size=%.0f", uploadID, filename, created["size"])

	// Get metadata.
	meta, err := client.UploadGet(ctx, uploadID)
	if err != nil {
		t.Fatalf("UploadGet: %v", err)
	}
	if meta["upload_id"] != uploadID {
		t.Errorf("metadata upload_id = %v, want %q", meta["upload_id"], uploadID)
	}
	if meta["filename"] != filename {
		t.Errorf("metadata filename = %v, want %q", meta["filename"], filename)
	}

	// Get content — should be byte-for-byte identical.
	content, err := client.UploadGetContent(ctx, uploadID)
	if err != nil {
		t.Fatalf("UploadGetContent: %v", err)
	}
	if string(content) != logContent {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", string(content), logContent)
	}

	// Content must contain our marker.
	if !strings.Contains(string(content), "e2e-upload-test-marker") {
		t.Error("content does not contain test marker")
	}

	t.Logf("upload CRUD OK: id=%s content_len=%d", uploadID, len(content))
}

// TestUploads_NotFound verifies that a 404 is returned for an unknown upload ID.
func TestUploads_NotFound(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.UploadGet(ctx, "ul_doesnotexist")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("UploadGet non-existent: want 404 error, got %v", err)
	}

	_, err = client.UploadGetContent(ctx, "ul_doesnotexist")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("UploadGetContent non-existent: want 404 error, got %v", err)
	}
}

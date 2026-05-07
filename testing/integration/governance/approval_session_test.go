//go:build integration

package governance

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// deleteReq sends a DELETE to base+path and returns the status code and body.
func deleteReq(t *testing.T, base, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, base+path, nil)
	if err != nil {
		t.Fatalf("build DELETE %s: %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// getStatus fetches base+path and returns status + body without failing on 4xx.
func getStatus(t *testing.T, base, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(base + path) //nolint:noctx
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// createSession is a test helper that POSTs to /v1/approval/sessions and
// returns the session_id. It fails the test on any non-201 response.
func createSession(t *testing.T, grantedBy string, expiresInSecs int, classes []string) string {
	t.Helper()
	resp := post(t, auditdAddr, "/v1/approval/sessions", map[string]any{
		"granted_by":      grantedBy,
		"expires_in_secs": expiresInSecs,
		"allowed_classes": classes,
	})
	id, _ := resp["session_id"].(string)
	if id == "" {
		t.Fatalf("POST /v1/approval/sessions did not return a session_id; got: %v", resp)
	}
	return id
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestApprovalSession_Create_AssignsIDWithPrefix(t *testing.T) {
	id := createSession(t, "boris", 1800, []string{"write", "destructive"})

	if !strings.HasPrefix(id, "aps_") {
		t.Errorf("session_id = %q, want aps_ prefix", id)
	}
}

func TestApprovalSession_Create_ReturnsExpiresAt(t *testing.T) {
	before := time.Now()
	resp := post(t, auditdAddr, "/v1/approval/sessions", map[string]any{
		"granted_by":      "alice",
		"expires_in_secs": 3600,
		"allowed_classes": []string{"write"},
	})

	expiresAt, _ := resp["expires_at"].(string)
	if expiresAt == "" {
		t.Fatal("expires_at missing from response")
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatalf("expires_at not RFC3339: %v", err)
	}
	// Should be roughly 1 hour from now (allow a few seconds of drift).
	expectedMin := before.Add(3590 * time.Second)
	expectedMax := before.Add(3610 * time.Second)
	if exp.Before(expectedMin) || exp.After(expectedMax) {
		t.Errorf("expires_at = %v; want roughly 1h from %v", exp, before)
	}
}

func TestApprovalSession_Create_MissingGrantedBy_Returns400(t *testing.T) {
	code, body := postStatus(t, auditdAddr, "/v1/approval/sessions", map[string]any{
		"expires_in_secs": 1800,
		"allowed_classes": []string{"write"},
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", code, body)
	}
}

func TestApprovalSession_Create_ZeroExpiry_Returns400(t *testing.T) {
	code, body := postStatus(t, auditdAddr, "/v1/approval/sessions", map[string]any{
		"granted_by":      "boris",
		"expires_in_secs": 0,
		"allowed_classes": []string{"write"},
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", code, body)
	}
}

func TestApprovalSession_Create_EmptyClasses_Returns400(t *testing.T) {
	code, body := postStatus(t, auditdAddr, "/v1/approval/sessions", map[string]any{
		"granted_by":      "boris",
		"expires_in_secs": 1800,
		"allowed_classes": []string{},
	})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", code, body)
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestApprovalSession_Get_RoundTrip(t *testing.T) {
	id := createSession(t, "charlie", 900, []string{"destructive"})

	resp := get(t, auditdAddr, "/v1/approval/sessions/"+id)

	if resp["session_id"] != id {
		t.Errorf("session_id = %v, want %q", resp["session_id"], id)
	}
	if resp["granted_by"] != "charlie" {
		t.Errorf("granted_by = %v, want charlie", resp["granted_by"])
	}
	classes, _ := resp["allowed_classes"].([]any)
	if len(classes) != 1 || classes[0] != "destructive" {
		t.Errorf("allowed_classes = %v, want [destructive]", classes)
	}
	if revoked, _ := resp["revoked"].(bool); revoked {
		t.Error("revoked should be false for a fresh session")
	}
}

func TestApprovalSession_Get_WithScope(t *testing.T) {
	resp := post(t, auditdAddr, "/v1/approval/sessions", map[string]any{
		"granted_by":      "ops-team",
		"expires_in_secs": 1800,
		"allowed_classes": []string{"write"},
		"scope":           "pbs_db_restart_triage",
	})
	id, _ := resp["session_id"].(string)

	stored := get(t, auditdAddr, "/v1/approval/sessions/"+id)
	if stored["scope"] != "pbs_db_restart_triage" {
		t.Errorf("scope = %v, want pbs_db_restart_triage", stored["scope"])
	}
}

func TestApprovalSession_Get_NotFound_Returns404(t *testing.T) {
	code, _ := getStatus(t, auditdAddr, "/v1/approval/sessions/aps_nonexistent")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

// ── Revoke ────────────────────────────────────────────────────────────────────

func TestApprovalSession_Revoke_SetsRevokedFlag(t *testing.T) {
	id := createSession(t, "bob", 1800, []string{"write"})

	code, body := deleteReq(t, auditdAddr, "/v1/approval/sessions/"+id)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body: %s", code, body)
	}

	stored := get(t, auditdAddr, "/v1/approval/sessions/"+id)
	if revoked, _ := stored["revoked"].(bool); !revoked {
		t.Error("revoked should be true after DELETE")
	}
}

func TestApprovalSession_Revoke_NotFound_Returns404(t *testing.T) {
	code, _ := deleteReq(t, auditdAddr, "/v1/approval/sessions/aps_doesnotexist")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestApprovalSession_Revoke_Idempotent(t *testing.T) {
	// Revoking a session that was already revoked should not cause a 500.
	// Current behavior: second DELETE returns 404 (already gone from RowsAffected
	// perspective) — this test documents that contract.
	id := createSession(t, "eve", 1800, []string{"write"})

	code1, _ := deleteReq(t, auditdAddr, "/v1/approval/sessions/"+id)
	if code1 != http.StatusNoContent {
		t.Fatalf("first DELETE status = %d, want 204", code1)
	}

	code2, _ := deleteReq(t, auditdAddr, "/v1/approval/sessions/"+id)
	// Second revoke on an already-revoked session: the UPDATE touches 0 rows
	// because revoked=1 → UPDATE SET revoked=1 WHERE session_id=? still
	// matches the row, so RowsAffected=1 → 204 again.
	// If the store changes to check revoked=0 precondition, update this test.
	if code2 != http.StatusNoContent && code2 != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 204 or 404", code2)
	}
}

// ── Expiry semantics ──────────────────────────────────────────────────────────

func TestApprovalSession_Expiry_FieldsRetained(t *testing.T) {
	// We can't wait for real expiry in a short test, but we verify the
	// expires_at field is stored and returned correctly so that the gateway's
	// IsValid() check operates on good data.
	const ttl = 7
	before := time.Now()
	id := createSession(t, "tester", ttl, []string{"write"})

	stored := get(t, auditdAddr, "/v1/approval/sessions/"+id)
	expiresAt, _ := stored["expires_at"].(string)
	if expiresAt == "" {
		t.Fatal("expires_at missing from GET response")
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatalf("expires_at not RFC3339: %v", err)
	}
	expectedMin := before.Add(time.Duration(ttl-2) * time.Second)
	expectedMax := before.Add(time.Duration(ttl+2) * time.Second)
	if exp.Before(expectedMin) || exp.After(expectedMax) {
		t.Errorf("expires_at = %v; want ~%ds from test start", exp, ttl)
	}
}

//go:build integration

// Tests for auditor and secbot HTTP polling mode.
//
// When governance.auditd.persistence.enabled=false on Kubernetes, auditor and
// secbot cannot share the Unix socket via emptyDir (emptyDir is per-pod). The
// Helm chart instead passes -audit-service=<url> so they poll auditd over HTTP.
//
// These tests verify the HTTP polling path end-to-end: start a real auditd,
// post events that should trigger security alerts, and confirm auditor/secbot
// detect them via HTTP polling (no Unix socket involved).

package governance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startAuditdOnPort starts a fresh auditd on the given port and returns its
// base URL. The process is killed via t.Cleanup.
func startAuditdOnPort(t *testing.T, port int) (baseURL string, dbPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	addr := fmt.Sprintf(":%d", port)
	baseURL = fmt.Sprintf("http://localhost:%d", port)
	dbPath = fmt.Sprintf("%s/audit.db", tmpDir)

	cmd := exec.Command(auditdBin,
		"-listen", addr,
		"-db", dbPath,
		"-socket", fmt.Sprintf("/tmp/audit_mon_%d.sock", port),
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start auditd on %s: %v", addr, err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	if !waitForReady(baseURL+"/health", 10*time.Second) {
		t.Fatalf("auditd on %s did not become ready within 10 s", addr)
	}
	return baseURL, dbPath
}

// postDestructiveEvent POSTs an audit event with action_class=destructive and
// no approval. Both auditor and secbot treat this as a security alert:
//   - auditor: "DESTRUCTIVE operation detected" (AlertCritical)
//   - secbot:  "unauthorized_destructive"
func postDestructiveEvent(t *testing.T, auditdURL string) {
	t.Helper()
	payload := map[string]any{
		"event_id":     fmt.Sprintf("evt_test_%d", time.Now().UnixNano()),
		"event_type":   "tool_call",
		"action_class": "destructive",
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"session":      map[string]any{"id": "sess_monitor_test", "user_id": "testuser"},
		"tool":         map[string]any{"name": "delete_database", "agent": "database-agent"},
		"input":        map[string]any{"user_query": "drop all tables"},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(auditdURL+"/v1/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST event: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST event: HTTP %d: %s", resp.StatusCode, b)
	}
}

// captureOutput starts cmd, captures its combined stdout+stderr, and returns
// a function that kills the process and returns the collected output.
func captureOutput(t *testing.T, cmd *exec.Cmd) func() string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		t.Fatalf("start %s: %v", cmd.Path, err)
	}
	pw.Close() // parent closes its copy; child holds the write end

	ch := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(pr)
		pr.Close()
		ch <- string(b)
	}()

	return func() string {
		cmd.Process.Kill()
		cmd.Wait()
		return <-ch
	}
}

// freeTCPAddr returns a free localhost address (host:port).
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// ─────────────────────────────────────────────────────────────────────────────
// Auditor HTTP polling mode
// ─────────────────────────────────────────────────────────────────────────────

// TestAuditorHTTPPollingMode verifies that auditor detects security alerts via
// HTTP polling when started with -audit-service=<url> (no socket).
//
// Regression test for: auditor given an emptyDir volume that cannot be shared
// across pods on Kubernetes — the Unix socket created by auditd is unreachable.
func TestAuditorHTTPPollingMode(t *testing.T) {
	auditdURL, _ := startAuditdOnPort(t, 19910)

	// Webhook receiver: auditor POSTs alerts here. Buffer generously so the
	// default branch never drops a delivery (auditor may send INFO/WARN alerts
	// for routine events before the critical one arrives).
	alertCh := make(chan map[string]any, 32)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			select {
			case alertCh <- payload:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	// Start auditor: no -socket flag → pure HTTP polling mode.
	// -webhook-all sends every alert level so we exercise the full pipeline;
	// the test drains all deliveries until it finds the expected critical one.
	cmd := exec.Command(auditorBin,
		"-audit-service="+auditdURL,
		"-webhook="+webhookSrv.URL,
		"-webhook-all",
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start auditor: %v", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	// One poll cycle baseline (auditor ignores pre-existing events on startup).
	time.Sleep(6 * time.Second)

	// Post an event that triggers AlertCritical.
	postDestructiveEvent(t, auditdURL)

	// Drain webhook deliveries for up to 30 s; pass when "destructive" appears.
	// The auditor may fire INFO/WARN alerts for earlier events before the
	// critical destructive alert arrives — we accept any order.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case alert := <-alertCh:
			msg, _ := alert["message"].(string)
			level, _ := alert["level"].(string)
			t.Logf("webhook received: level=%s message=%q", level, msg)
			if strings.Contains(strings.ToLower(msg), "destructive") {
				return // test passes
			}
		case <-deadline:
			t.Fatal("auditor did not fire destructive alert via webhook within 30 s in HTTP polling mode")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Secbot HTTP polling mode
// ─────────────────────────────────────────────────────────────────────────────

// TestSecbotHTTPPollingMode verifies that secbot detects security alerts via
// HTTP polling when started with -audit-service=<url> (no socket).
func TestSecbotHTTPPollingMode(t *testing.T) {
	auditdURL, _ := startAuditdOnPort(t, 19911)

	cmd := exec.Command(secbotBin,
		"-audit-service="+auditdURL,
		"-gateway=http://127.0.0.1:19999", // unreachable; dry-run prevents calls
		"-listen="+freeTCPAddr(t),
		"-dry-run=true",
		"-verbose=true",
	)
	collect := captureOutput(t, cmd)

	// Baseline (secbot marks pre-existing events as seen, no alerts).
	time.Sleep(6 * time.Second)

	// Post a destructive event → "unauthorized_destructive" alert.
	postDestructiveEvent(t, auditdURL)

	// Allow 2 more poll cycles for detection.
	time.Sleep(12 * time.Second)

	output := collect()
	t.Logf("secbot output:\n%s", truncateOutput(output, 2000))

	if !strings.Contains(output, "SECURITY ALERT") {
		t.Error("secbot did not log 'SECURITY ALERT' in HTTP polling mode")
	}
	if !strings.Contains(output, "unauthorized_destructive") {
		t.Error("expected alert type 'unauthorized_destructive' in secbot output")
	}
	if !strings.Contains(output, "[DRY RUN]") {
		t.Error("expected '[DRY RUN]' in secbot output (dry-run mode)")
	}
}

// TestSecbotHTTPPollingReconnect verifies that secbot continues processing events
// after auditd restarts. The HTTP polling retry loop should recover automatically.
func TestSecbotHTTPPollingReconnect(t *testing.T) {
	port := 19912
	auditdURL := fmt.Sprintf("http://localhost:%d", port)
	tmpDir := t.TempDir()
	dbPath := fmt.Sprintf("%s/audit.db", tmpDir)
	socketPath := fmt.Sprintf("/tmp/audit_mon_%d.sock", port)

	startAuditdProc := func() *exec.Cmd {
		cmd := exec.Command(auditdBin,
			"-listen", fmt.Sprintf(":%d", port),
			"-db", dbPath,
			"-socket", socketPath,
		)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start auditd: %v", err)
		}
		if !waitForReady(auditdURL+"/health", 10*time.Second) {
			cmd.Process.Kill()
			t.Fatal("auditd did not become ready within 10 s")
		}
		return cmd
	}

	auditdProc := startAuditdProc()

	secbotCmd := exec.Command(secbotBin,
		"-audit-service="+auditdURL,
		"-gateway=http://127.0.0.1:19999",
		"-listen="+freeTCPAddr(t),
		"-dry-run=true",
		"-verbose=true",
	)
	collect := captureOutput(t, secbotCmd)

	// Baseline.
	time.Sleep(6 * time.Second)

	// First event — should be detected.
	t.Log("posting first event (before restart)...")
	postDestructiveEvent(t, auditdURL)
	time.Sleep(6 * time.Second)

	// Restart auditd.
	t.Log("restarting auditd...")
	auditdProc.Process.Kill()
	auditdProc.Wait()
	time.Sleep(3 * time.Second) // let secbot see poll failures

	auditdProc2 := startAuditdProc()
	defer func() { auditdProc2.Process.Kill(); auditdProc2.Wait() }()

	// Second event after restart — secbot should reconnect and detect it.
	t.Log("posting second event (after restart)...")
	postDestructiveEvent(t, auditdURL)
	time.Sleep(12 * time.Second) // poll + reconnect headroom

	output := collect()
	t.Logf("secbot output:\n%s", truncateOutput(output, 3000))

	count := strings.Count(output, "SECURITY ALERT")
	if count < 2 {
		t.Errorf("expected ≥2 security alerts (one before restart, one after); got %d\n"+
			"Check that HTTP polling retry recovers after auditd restart.", count)
	}
}

// truncateOutput keeps the first and last maxLen/2 bytes of long output.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	half := maxLen / 2
	return s[:half] + "\n...[truncated]...\n" + s[len(s)-half:]
}

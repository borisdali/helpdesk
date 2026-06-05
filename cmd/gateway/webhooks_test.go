package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/a2aclient"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
)

// buildHubSig computes the expected X-Hub-Signature-256 for a body and secret.
func buildHubSig(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// makeWebhookGateway returns a Gateway wired with the given auditURL and secret config.
func makeWebhookGateway(auditURL, secret, resolveBranch string) *Gateway {
	return &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		gitWebhookCfg: GitWebhookConfig{
			Secret:        secret,
			ResolveBranch: resolveBranch,
		},
		auditURL: auditURL,
	}
}

// githubMergePayload returns a minimal GitHub pull_request merge event body.
func githubMergePayload(targetBranch string) []byte {
	body, _ := json.Marshal(map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"merged": true,
			"base":   map[string]string{"ref": targetBranch},
		},
	})
	return body
}

// gitlabMergePayload returns a minimal GitLab merge_request event body.
func gitlabMergePayload(targetBranch string) []byte {
	body, _ := json.Marshal(map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]string{
			"state":         "merged",
			"target_branch": targetBranch,
		},
	})
	return body
}

// genericMergePayload returns a minimal generic merge event body.
func genericMergePayload(branch string) []byte {
	body, _ := json.Marshal(map[string]string{"branch": branch})
	return body
}

// ---- extractMergedBranch unit tests ----

func TestExtractMergedBranch_GitHub(t *testing.T) {
	gw := &Gateway{}
	body := githubMergePayload("approved/gate/plr_abc123")
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if got := gw.extractMergedBranch(req, body); got != "approved/gate/plr_abc123" {
		t.Errorf("got %q, want approved/gate/plr_abc123", got)
	}
}

func TestExtractMergedBranch_GitHub_NotMerged(t *testing.T) {
	gw := &Gateway{}
	body, _ := json.Marshal(map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"merged": false,
			"base":   map[string]string{"ref": "approved/gate/plr_abc123"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if got := gw.extractMergedBranch(req, body); got != "" {
		t.Errorf("got %q, want empty for non-merged PR", got)
	}
}

func TestExtractMergedBranch_GitLab(t *testing.T) {
	gw := &Gateway{}
	body := gitlabMergePayload("approved/fleet/apr_xyz789")
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if got := gw.extractMergedBranch(req, body); got != "approved/fleet/apr_xyz789" {
		t.Errorf("got %q, want approved/fleet/apr_xyz789", got)
	}
}

func TestExtractMergedBranch_GitLab_NotMerged(t *testing.T) {
	gw := &Gateway{}
	body, _ := json.Marshal(map[string]any{
		"object_kind":       "merge_request",
		"object_attributes": map[string]string{"state": "opened", "target_branch": "approved/gate/plr_x"},
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if got := gw.extractMergedBranch(req, body); got != "" {
		t.Errorf("got %q, want empty for non-merged MR", got)
	}
}

func TestExtractMergedBranch_Generic(t *testing.T) {
	gw := &Gateway{}
	body := genericMergePayload("approved/gate/plr_gen01")
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if got := gw.extractMergedBranch(req, body); got != "approved/gate/plr_gen01" {
		t.Errorf("got %q, want approved/gate/plr_gen01", got)
	}
}

func TestExtractMergedBranch_InvalidJSON(t *testing.T) {
	gw := &Gateway{}
	body := []byte("not json")
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if got := gw.extractMergedBranch(req, body); got != "" {
		t.Errorf("got %q, want empty for invalid JSON", got)
	}
}

// ---- verifyWebhookSignature unit tests ----

func TestVerifyWebhookSignature_GitHub_Valid(t *testing.T) {
	gw := &Gateway{gitWebhookCfg: GitWebhookConfig{Secret: "s3cr3t"}}
	body := []byte(`{"action":"closed"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", buildHubSig(body, "s3cr3t"))
	if !gw.verifyWebhookSignature(req, body) {
		t.Error("expected valid signature to pass")
	}
}

func TestVerifyWebhookSignature_GitHub_Wrong(t *testing.T) {
	gw := &Gateway{gitWebhookCfg: GitWebhookConfig{Secret: "s3cr3t"}}
	body := []byte(`{"action":"closed"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", buildHubSig(body, "wrong-secret"))
	if gw.verifyWebhookSignature(req, body) {
		t.Error("expected wrong signature to fail")
	}
}

func TestVerifyWebhookSignature_GitLab_Valid(t *testing.T) {
	gw := &Gateway{gitWebhookCfg: GitWebhookConfig{Secret: "gitlab-secret"}}
	body := []byte(`{"object_kind":"merge_request"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "gitlab-secret")
	if !gw.verifyWebhookSignature(req, body) {
		t.Error("expected valid GitLab token to pass")
	}
}

func TestVerifyWebhookSignature_NoHeaders_Fails(t *testing.T) {
	gw := &Gateway{gitWebhookCfg: GitWebhookConfig{Secret: "s3cr3t"}}
	body := []byte(`{"action":"closed"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	// No signature header at all.
	if gw.verifyWebhookSignature(req, body) {
		t.Error("expected missing signature to fail")
	}
}

// ---- handleGitWebhook integration tests ----

func TestHandleGitWebhook_InvalidHMAC_Returns401(t *testing.T) {
	gw := makeWebhookGateway("", "correct-secret", "")
	body := githubMergePayload("approved/gate/plr_x")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", buildHubSig(body, "wrong-secret"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestHandleGitWebhook_NonMatchingBranch_Returns200(t *testing.T) {
	gw := makeWebhookGateway("", "", "")
	body := githubMergePayload("main") // not an approved/ branch
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200 for non-matching branch", rec.Code)
	}
}

func TestHandleGitWebhook_UnknownPrefix_Returns200(t *testing.T) {
	gw := makeWebhookGateway("", "", "")
	// Branch starts with "approved/" but has no known type segment.
	body := githubMergePayload("approved/unknown/foo")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200 for unknown branch suffix", rec.Code)
	}
}

func TestHandleGitWebhook_CustomResolveBranch(t *testing.T) {
	gw := makeWebhookGateway("", "", "merged/")
	// Custom prefix: "merged/" instead of default "approved/"
	body := githubMergePayload("main") // doesn't start with "merged/"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200 no-op", rec.Code)
	}
}

func TestHandleGitWebhook_GateRoute_CallsAuditd(t *testing.T) {
	const runID = "plr_abc123"

	// Track which auditd paths are called.
	var calledPaths []string
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPaths = append(calledPaths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/playbook-runs/"+runID):
			// Return a gate_pending run so handleProceedEscalation proceeds.
			run := audit.PlaybookRun{
				RunID:      runID,
				PlaybookID: "pbs_vacuum_triage_v1",
				Outcome:    audit.OutcomeGatePending,
				StartedAt:  time.Now(),
			}
			json.NewEncoder(w).Encode(run) //nolint:errcheck
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/playbook-runs/"+runID):
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
		default:
			// Audit events, etc. — just 200 OK.
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
		}
	}))
	defer auditd.Close()

	gw := makeWebhookGateway(auditd.URL, "", "")
	body := githubMergePayload("approved/gate/" + runID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)

	// The gateway should have fetched the run from auditd.
	foundFetch := false
	for _, p := range calledPaths {
		if strings.Contains(p, runID) {
			foundFetch = true
			break
		}
	}
	if !foundFetch {
		t.Errorf("auditd was not called with run ID %q; got paths: %v", runID, calledPaths)
	}
}

func TestHandleGitWebhook_FleetRoute_CallsAuditd(t *testing.T) {
	const approvalID = "apr_fleet01"

	var calledPaths []string
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPaths = append(calledPaths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
	}))
	defer auditd.Close()

	gw := makeWebhookGateway(auditd.URL, "", "")
	body := githubMergePayload("approved/fleet/" + approvalID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)

	foundApproval := false
	for _, p := range calledPaths {
		if strings.Contains(p, approvalID) {
			foundApproval = true
			break
		}
	}
	if !foundApproval {
		t.Errorf("auditd was not called with approval ID %q; got paths: %v", approvalID, calledPaths)
	}
}

func TestHandleGitWebhook_GitLabMerge_Works(t *testing.T) {
	const runID = "plr_gitlab01"

	var calledPaths []string
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPaths = append(calledPaths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/playbook-runs/"+runID) {
			run := audit.PlaybookRun{
				RunID: runID, PlaybookID: "pbs_triage_v1",
				Outcome: audit.OutcomeGatePending, StartedAt: time.Now(),
			}
			json.NewEncoder(w).Encode(run) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
	}))
	defer auditd.Close()

	gw := makeWebhookGateway(auditd.URL, "", "")
	body := gitlabMergePayload("approved/gate/" + runID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)

	foundFetch := false
	for _, p := range calledPaths {
		if strings.Contains(p, runID) {
			foundFetch = true
			break
		}
	}
	if !foundFetch {
		t.Errorf("auditd was not called with run ID %q; got paths: %v", runID, calledPaths)
	}
}

func TestHandleGitWebhook_HMACSkippedWhenSecretEmpty(t *testing.T) {
	// When Secret is empty, HMAC validation is skipped — even bad/missing sigs pass.
	gw := makeWebhookGateway("", "", "")
	body := githubMergePayload("other-branch")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/git", bytes.NewReader(body))
	// Deliberately wrong signature; should be ignored.
	req.Header.Set("X-Hub-Signature-256", "sha256=badhash")
	rec := httptest.NewRecorder()
	gw.handleGitWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200 when no secret configured", rec.Code)
	}
}

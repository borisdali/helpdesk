package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// GitWebhookConfig holds the configuration for the inbound git webhook adapter.
type GitWebhookConfig struct {
	// Secret is the HMAC-SHA256 key used to verify webhook payloads.
	// When empty, signature checking is skipped (not recommended for production).
	Secret string
	// ResolveBranch is the branch name prefix that triggers decision resolution.
	// Merges targeting a branch that starts with this prefix are acted on.
	// Default: "approved/".
	ResolveBranch string
}

// handleGitWebhook handles POST /api/v1/webhooks/git.
//
// It accepts merge/push events from GitHub, GitLab, Gitea, or a generic
// JSON payload and resolves any decision whose ID is encoded in the target
// branch name using the convention:
//
//	approved/gate/{runID}    → approve playbook gate
//	approved/fleet/{id}      → approve fleet job
//
// HMAC validation uses X-Hub-Signature-256 (GitHub/Gitea) or
// X-Gitlab-Token (GitLab). If Secret is empty, validation is skipped.
func (g *Gateway) handleGitWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	if g.gitWebhookCfg.Secret != "" {
		if !g.verifyWebhookSignature(r, body) {
			writeError(w, http.StatusUnauthorized, "invalid webhook signature")
			return
		}
	}

	branch := g.extractMergedBranch(r, body)
	if branch == "" {
		// Not a merge event or branch could not be extracted — ack silently.
		w.WriteHeader(http.StatusOK)
		return
	}

	prefix := g.gitWebhookCfg.ResolveBranch
	if prefix == "" {
		prefix = "approved/"
	}
	if !strings.HasPrefix(branch, prefix) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Strip the prefix and parse the decision ID.
	// Remaining form: "gate/{runID}" or "fleet/{approvalID}"
	tail := strings.TrimPrefix(branch, prefix)
	var decisionID string
	switch {
	case strings.HasPrefix(tail, "gate/"):
		decisionID = "gate:" + strings.TrimPrefix(tail, "gate/")
	case strings.HasPrefix(tail, "fleet/"):
		decisionID = "fleet:" + strings.TrimPrefix(tail, "fleet/")
	default:
		slog.Info("git webhook: unrecognised branch suffix, ignoring", "branch", branch)
		w.WriteHeader(http.StatusOK)
		return
	}

	slog.Info("git webhook: resolving decision from merge", "branch", branch, "decision_id", decisionID)

	resolveBody, _ := json.Marshal(map[string]any{
		"resolution":  "approved",
		"resolved_by": "git-webhook",
	})
	r2 := r.Clone(r.Context())
	r2.SetPathValue("id", decisionID)
	r2.Body = io.NopCloser(bytes.NewReader(resolveBody))
	r2.ContentLength = int64(len(resolveBody))
	g.handleResolveDecision(w, r2)
}

// verifyWebhookSignature checks HMAC signatures from GitHub/Gitea or GitLab.
func (g *Gateway) verifyWebhookSignature(r *http.Request, body []byte) bool {
	mac := hmac.New(sha256.New, []byte(g.gitWebhookCfg.Secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// GitHub / Gitea style
	if sig := r.Header.Get("X-Hub-Signature-256"); sig != "" {
		return hmac.Equal([]byte(sig), []byte(expected))
	}
	// GitLab sends the raw secret in X-Gitlab-Token
	if token := r.Header.Get("X-Gitlab-Token"); token != "" {
		return hmac.Equal([]byte(token), []byte(g.gitWebhookCfg.Secret))
	}
	return false
}

// extractMergedBranch parses the target branch name from a merge/push event.
// Returns "" when the event is not a completed merge.
func (g *Gateway) extractMergedBranch(r *http.Request, body []byte) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}

	// GitHub / Gitea: {"action":"closed","pull_request":{"merged":true,"base":{"ref":"approved/gate/plr_..."}}}
	if action, ok := raw["action"]; ok {
		var actionStr string
		if err := json.Unmarshal(action, &actionStr); err != nil || actionStr != "closed" {
			return ""
		}
		var pr struct {
			Merged bool `json:"merged"`
			Base   struct {
				Ref string `json:"ref"`
			} `json:"base"`
		}
		if prRaw, ok := raw["pull_request"]; ok {
			if err := json.Unmarshal(prRaw, &pr); err == nil && pr.Merged {
				return pr.Base.Ref
			}
		}
		return ""
	}

	// GitLab: {"object_kind":"merge_request","object_attributes":{"state":"merged","target_branch":"..."}}
	if okRaw, ok := raw["object_kind"]; ok {
		var kind string
		if err := json.Unmarshal(okRaw, &kind); err != nil || kind != "merge_request" {
			return ""
		}
		var attrs struct {
			State        string `json:"state"`
			TargetBranch string `json:"target_branch"`
		}
		if attrsRaw, ok := raw["object_attributes"]; ok {
			if err := json.Unmarshal(attrsRaw, &attrs); err == nil && attrs.State == "merged" {
				return attrs.TargetBranch
			}
		}
		return ""
	}

	// Generic fallback: {"branch":"approved/gate/plr_..."}
	if branchRaw, ok := raw["branch"]; ok {
		var branch string
		if err := json.Unmarshal(branchRaw, &branch); err == nil {
			return branch
		}
	}

	return ""
}

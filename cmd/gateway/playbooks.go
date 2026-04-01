package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// proxyToAuditd forwards the request to the auditd service at the given path
// and copies the response back to w. The request body is forwarded as-is.
func (g *Gateway) proxyToAuditd(w http.ResponseWriter, r *http.Request, auditPath string) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}
	url := strings.TrimSuffix(g.auditURL, "/") + auditPath

	// Build forwarded request.
	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build proxy request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// Authenticate to auditd using the gateway's own service account key.
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	// Forward the originating user identity so auditd can record who made the change.
	if user := r.Header.Get("X-User"); user != "" {
		req.Header.Set("X-User", user)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auditd request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read auditd response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody) //nolint:errcheck
}

// handlePlaybookCreate proxies POST /api/v1/fleet/playbooks → auditd.
func (g *Gateway) handlePlaybookCreate(w http.ResponseWriter, r *http.Request) {
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks")
}

// handlePlaybookList proxies GET /api/v1/fleet/playbooks → auditd.
func (g *Gateway) handlePlaybookList(w http.ResponseWriter, r *http.Request) {
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks")
}

// handlePlaybookGet proxies GET /api/v1/fleet/playbooks/{id} → auditd.
func (g *Gateway) handlePlaybookGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id)
}

// handlePlaybookUpdate proxies PUT /api/v1/fleet/playbooks/{id} → auditd.
func (g *Gateway) handlePlaybookUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id)
}

// handlePlaybookDelete proxies DELETE /api/v1/fleet/playbooks/{id} → auditd.
func (g *Gateway) handlePlaybookDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	g.proxyToAuditd(w, r, "/v1/fleet/playbooks/"+id)
}

// handlePlaybookRun handles POST /api/v1/fleet/playbooks/{id}/run.
// It fetches the playbook from auditd, calls the fleet planner, and returns
// a FleetPlanResponse — identical to /api/v1/fleet/plan but without requiring
// the caller to supply the description.
func (g *Gateway) handlePlaybookRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing playbook ID")
		return
	}

	if g.plannerLLM == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet planner LLM not configured")
		return
	}
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}

	// Fetch the playbook from auditd.
	pb, err := g.fetchPlaybook(r.Context(), id)
	if err != nil {
		slog.Error("handlePlaybookRun: failed to fetch playbook", "id", id, "err", err)
		writeError(w, http.StatusNotFound, fmt.Sprintf("playbook %q not found: %v", id, err))
		return
	}

	// Build a synthetic FleetPlanRequest and call the planner by forwarding
	// to handleFleetPlan. We do this by constructing a new request with the
	// playbook's description and target hints.
	planReqBody, err := json.Marshal(FleetPlanRequest{
		Description: pb.Description,
		TargetHints: pb.TargetHints,
		Guidance:    pb.Guidance,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build plan request: "+err.Error())
		return
	}
	planReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, r.URL.Path, strings.NewReader(string(planReqBody)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build plan request: "+err.Error())
		return
	}
	// Copy identity headers so the planner audit record captures the right principal.
	for _, h := range []string{"X-User", "X-API-Key", "Authorization"} {
		if v := r.Header.Get(h); v != "" {
			planReq.Header.Set(h, v)
		}
	}
	g.handleFleetPlan(w, planReq)
}

// fetchPlaybook retrieves a single playbook record from auditd.
func (g *Gateway) fetchPlaybook(ctx context.Context, id string) (*audit.Playbook, error) {
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbooks/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx2)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var pb audit.Playbook
	if err := json.Unmarshal(body, &pb); err != nil {
		return nil, err
	}
	return &pb, nil
}

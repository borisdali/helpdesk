package main

import (
	"io"
	"net/http"
	"strings"
)

// handleUploadCreate proxies POST /api/v1/fleet/uploads → auditd.
// Unlike proxyToAuditd, this preserves the original Content-Type header so
// the multipart boundary is forwarded intact.
func (g *Gateway) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/uploads"

	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build proxy request: "+err.Error())
		return
	}
	// Forward the original Content-Type (includes multipart boundary).
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
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

// handleUploadGet proxies GET /api/v1/fleet/uploads/{uploadID} → auditd.
func (g *Gateway) handleUploadGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uploadID")
	g.proxyToAuditd(w, r, "/v1/uploads/"+id)
}

// handleUploadGetContent proxies GET /api/v1/fleet/uploads/{uploadID}/content → auditd,
// preserving the auditd response Content-Type (text/plain, not application/json).
func (g *Gateway) handleUploadGetContent(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "auditd URL not configured")
		return
	}
	id := r.PathValue("uploadID")
	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/uploads/" + id + "/content"

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build proxy request: "+err.Error())
		return
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "auditd request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Forward the content headers from auditd (Content-Type, Content-Disposition).
	for _, h := range []string{"Content-Type", "Content-Disposition"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

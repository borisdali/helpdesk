package audit

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// FetchIncidentByEntryRunID queries auditd's GET /v1/incidents/by-run/{runID}
// for the incidents-table row whose entry_run_id matches runID, mirroring
// FetchToolExecutionEvents/FetchDelegationVerificationEvents/
// FetchObjectiveEvidenceEvents above in package layout and failure handling:
// fails open (returns nil, logs at Debug) on any transport error, decode
// error, or a 404 (no incident row exists for this run — true for every
// run predating the v0.29 incident-entity redesign, and for runs that were
// never tracked as an incident at all, e.g. an ad-hoc diagnostic query with
// no gate/remediation chain). Single attempt, no retry: unlike
// FetchObjectiveEvidenceEvents's async-propagation race, the incidents-table
// row is created synchronously by the same request path that creates the
// playbook run itself, so there is no equivalent write-read race here.
//
// Used by cmd/gateway's handleGetIncident to close the "list vs. get"
// split-brain found in the v0.29 verification round: GET /api/v1/incidents
// (list) was rewired to read the new incidents table and carries
// Origin/Status/Attribution/BundlePath/DraftPlaybookID, but GET
// /api/v1/incidents/{runID} (single item) still builds its response purely
// from playbook_runs/audit events and never looked at the new table at all.
func FetchIncidentByEntryRunID(auditURL, apiKey, runID string) *Incident {
	if auditURL == "" || runID == "" {
		return nil
	}
	reqURL := strings.TrimRight(auditURL, "/") + "/v1/incidents/by-run/" + runID

	req, err := http.NewRequest(http.MethodGet, reqURL, nil) //nolint:noctx
	if err != nil {
		slog.Debug("fetch incident by entry_run_id: build request failed", "run_id", runID, "err", err)
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("fetch incident by entry_run_id: request failed", "run_id", runID, "err", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		slog.Debug("fetch incident by entry_run_id: unexpected status", "run_id", runID, "status", resp.StatusCode)
		return nil
	}

	var inc Incident
	if err := json.NewDecoder(resp.Body).Decode(&inc); err != nil {
		slog.Debug("fetch incident by entry_run_id: decode failed", "run_id", runID, "err", err)
		return nil
	}
	return &inc
}

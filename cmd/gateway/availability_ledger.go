package main

// GET /api/v1/availability-ledger?database=<key|name|connection string>: how
// much of a database's downtime budget the real incidents in its window have
// spent (internal/availability). The gateway is where the three inputs meet:
// the objectives (infrastructure config), the incidents and the series of the
// runs they spanned (auditd), and which playbook series costs availability
// (playbooks.AvailabilityImpact).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

// ledgerIncidents is auditd's incident listing with each incident's series.
type ledgerIncidents struct {
	Incidents []struct {
		audit.Incident
		SeriesIDs []string `json:"series_ids"`
	} `json:"incidents"`
	Count     int  `json:"count"`
	Truncated bool `json:"truncated"`
}

// fetchLedgerIncidents reads real incidents from auditd; query holds the filters.
func (g *Gateway) fetchLedgerIncidents(ctx context.Context, query url.Values) (*ledgerIncidents, error) {
	if g.auditURL == "" {
		return nil, errors.New("no audit service is configured")
	}
	query.Set("origin", audit.IncidentOriginReal)
	query.Set("limit", "200")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(g.auditURL, "/")+"/v1/incidents?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx2))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("auditd returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out ledgerIncidents
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// availabilityLedger builds the ledger for one database key at a moment.
func (g *Gateway) availabilityLedger(ctx context.Context, key string, now time.Time) (*availability.Ledger, error) {
	res, ok := g.infra.ResolveObjectives(key)
	if !ok {
		return nil, fmt.Errorf("%q is not a configured database", key)
	}
	budget := res.Budget()
	window := budget.Window
	if window <= 0 {
		window = infra.DefaultAvailabilityWindow
	}
	since := now.Add(-window).UTC().Format(time.RFC3339)

	mine, err := g.fetchLedgerIncidents(ctx, url.Values{"target_key": {key}, "since": {since}, "include": {"series"}})
	if err != nil {
		return nil, err
	}
	// Incidents that could not be tied to any database are in no ledger. Say how
	// many there are, so a ledger is not read as the whole picture.
	loose, err := g.fetchLedgerIncidents(ctx, url.Values{"unattributed": {"true"}, "since": {since}})
	if err != nil {
		return nil, err
	}

	incs := make([]availability.Incident, 0, len(mine.Incidents))
	for _, i := range mine.Incidents {
		series := i.SeriesIDs
		if len(series) == 0 && i.SeriesID != "" {
			series = []string{i.SeriesID}
		}
		incs = append(incs, availability.Incident{
			ID: i.IncidentID, SeriesIDs: series, Status: i.Status, Origin: i.Origin,
			DetectedAt: i.DetectedAt, ResolvedAt: i.ResolvedAt, TargetInput: i.TargetInput,
		})
	}
	l := availability.Compute(availability.Params{
		Database: key, Budget: budget, Incidents: incs, Now: now, Classify: playbooks.AvailabilityImpact,
		Truncated: mine.Truncated || loose.Truncated, Unattributed: loose.Count,
	})
	return &l, nil
}

func (g *Gateway) handleAvailabilityLedger(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("database"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "database is required: a db_servers key, a name or a connection string")
		return
	}
	if g.infra == nil {
		writeError(w, http.StatusNotFound, "no infrastructure configuration is loaded, so no database has objectives")
		return
	}
	key, err := g.infra.ResolveTarget(target)
	if err != nil {
		var amb *infra.AmbiguousTargetError
		switch {
		case errors.As(err, &amb):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusNotFound, fmt.Sprintf("database %q is not in the infrastructure configuration", target))
		}
		return
	}
	l, err := g.availabilityLedger(r.Context(), key, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not read the incidents the ledger is built from: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, l)
}

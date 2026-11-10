package availability

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
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

// Client reads the incidents a ledger is built from out of auditd. The gateway
// (its ledger endpoint) and the sysadmin agent (the gate on a restore) both use
// it, so the two cannot disagree about what the ledger says.
type Client struct {
	// URL is auditd's base URL; APIKey is the service key sent as a bearer
	// token, when there is one.
	URL    string
	APIKey string
	// HTTP is the client to use; a 30-second one when nil.
	HTTP *http.Client
}

// ErrNoAudit is returned when the client has no auditd to read from. A ledger
// built from nothing would claim a database had spent none of its budget, so it
// is an error, never an empty ledger.
var ErrNoAudit = errors.New("no audit service is configured")

// pageLimit is the most incidents one auditd page returns.
const pageLimit = 200

// Incidents lists real incidents matching query (target_key, unattributed,
// since, include). It asks for origin=real and a full page; Listing.Truncated
// says when that page was full.
func (c *Client) Incidents(ctx context.Context, query url.Values) (*audit.IncidentListing, error) {
	if c == nil || c.URL == "" {
		return nil, ErrNoAudit
	}
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("origin", audit.IncidentOriginReal)
	q.Set("limit", fmt.Sprint(pageLimit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.URL, "/")+"/v1/incidents?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("auditd returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out audit.IncidentListing
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ledger builds the ledger of the database with this infrastructure key at now,
// over the window the budget gives (the default window when it gives none): its
// real incidents, and a count of the ones no database could be found for.
func (c *Client) Ledger(ctx context.Context, key string, budget infra.AvailabilityBudget, now time.Time) (*Ledger, error) {
	window := budget.Window
	if window <= 0 {
		window = infra.DefaultAvailabilityWindow
	}
	since := now.Add(-window).UTC().Format(time.RFC3339)

	mine, err := c.Incidents(ctx, url.Values{"target_key": {key}, "since": {since}, "include": {"series"}})
	if err != nil {
		return nil, err
	}
	// Incidents that could not be tied to any database are in no ledger. Say how
	// many there are, so a ledger is not read as the whole picture.
	loose, err := c.Incidents(ctx, url.Values{"unattributed": {"true"}, "since": {since}})
	if err != nil {
		return nil, err
	}

	incs := make([]Incident, 0, len(mine.Incidents))
	for _, i := range mine.Incidents {
		if i.Incident == nil {
			continue
		}
		series := i.SeriesIDs
		if len(series) == 0 && i.SeriesID != "" {
			series = []string{i.SeriesID}
		}
		incs = append(incs, Incident{
			ID: i.IncidentID, SeriesIDs: series, Status: i.Status, Origin: i.Origin,
			DetectedAt: i.DetectedAt, ResolvedAt: i.ResolvedAt, TargetInput: i.TargetInput,
		})
	}
	l := Compute(Params{
		Database: key, Budget: budget, Incidents: incs, Now: now, Classify: playbooks.AvailabilityImpact,
		Truncated: mine.Truncated || loose.Truncated, Unattributed: loose.Count,
	})
	return &l, nil
}

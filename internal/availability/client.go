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

// ticketEventLimit is how many recovery decisions one ticket lookup reads. A
// window with more than that loses the tickets of the oldest ones.
const ticketEventLimit = 1000

// ApprovalTickets reads the tickets point-in-time approvals were given under
// since a moment, by the run each approval belongs to. A run approved under no
// ticket is not in the result. It reads the gate-stage recovery decisions from
// auditd, which is the one place the ticket is recorded.
func (c *Client) ApprovalTickets(ctx context.Context, since time.Time) (map[string]string, error) {
	if c == nil || c.URL == "" {
		return nil, ErrNoAudit
	}
	q := url.Values{}
	q.Set("event_type", string(audit.EventTypeRecoveryDecision))
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("limit", fmt.Sprint(ticketEventLimit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.URL, "/")+"/v1/events?"+q.Encode(), nil)
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
	var events []audit.Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range events {
		d := e.RecoveryDecision
		if d == nil || d.Stage != audit.RecoveryStageGate || d.RunID == "" || d.Approval == nil || d.Approval.Ticket == "" {
			continue
		}
		out[d.RunID] = d.Approval.Ticket
	}
	return out, nil
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
	// many there are, so a ledger is not read as the whole picture. Real incidents
	// only: a faulttest incident never counts against a budget, so one without a
	// database is no gap in any ledger (a cluster reinstalled over an audit store
	// that outlived it reported 8 such leftovers from earlier test runs as missing).
	loose, err := c.Incidents(ctx, url.Values{"unattributed": {"true"}, "origin": {"real"}, "since": {since}})
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
			EntryRunID: i.EntryRunID,
		})
	}
	l := Compute(Params{
		Database: key, Budget: budget, Incidents: incs, Now: now, Classify: playbooks.AvailabilityImpact,
		Truncated: mine.Truncated || loose.Truncated, Unattributed: loose.Count,
	})
	return &l, nil
}

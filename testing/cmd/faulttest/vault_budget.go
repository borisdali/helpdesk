package main

// `faulttest vault budget <database>`: how much of a database's downtime budget
// its recorded incidents have spent in the SLO window (the gateway's
// GET /api/v1/availability-ledger, internal/availability). Formatting is pure,
// so it can be checked without a gateway.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

func vaultBudget(args []string) {
	fs := flag.NewFlagSet("vault budget", flag.ExitOnError)
	cfg := loadConfig(fs, args)
	if cfg.GatewayURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --gateway URL is required for vault budget")
		os.Exit(1)
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: faulttest vault budget <database>   (a db_servers key, a name or a connection string)")
		os.Exit(1)
	}
	l, err := fetchAvailabilityLedger(cfg.GatewayURL, cfg.GatewayAPIKey, fs.Args()[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(formatLedger(*l))
}

func fetchAvailabilityLedger(gatewayURL, apiKey, database string) (*availability.Ledger, error) {
	u := strings.TrimSuffix(gatewayURL, "/") + "/api/v1/availability-ledger?" + neturl.Values{"database": {database}}.Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var l availability.Ledger
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &l, nil
}

// span renders seconds that may be negative (an overspent budget).
func ledgerSpan(s int64) string {
	if s < 0 {
		return "-" + rdSpan(-s)
	}
	return rdSpan(s)
}

func formatLedger(l availability.Ledger) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Availability budget: %s\n", l.Database)
	fmt.Fprintf(&b, "  Window   %s [%s], as of %s\n", infra.FormatSpan(time.Duration(l.WindowSeconds)*time.Second), l.WindowSource, l.AsOf.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "  Budget   %s\n", l.BudgetDetail)

	spent := fmt.Sprintf("%s in %d outage incident(s)", ledgerSpan(l.ChargedSeconds), len(l.Charged))
	if l.UsedShare != nil {
		spent += fmt.Sprintf(", %.0f%% of the budget", *l.UsedShare*100)
	}
	fmt.Fprintf(&b, "  Spent    %s\n", spent)
	if l.RemainingSeconds != nil {
		rem := "  Left     " + ledgerSpan(*l.RemainingSeconds)
		if l.Overspent {
			rem += "   ⚠ OVERSPENT: the budget is used up"
		}
		fmt.Fprintln(&b, rem)
		if len(l.Unconfirmed) > 0 && l.RemainingWorstCaseSeconds != nil {
			fmt.Fprintf(&b, "           %s if the unconfirmed outages below count in full\n", ledgerSpan(*l.RemainingWorstCaseSeconds))
		}
	}

	section := func(title string, entries []availability.Entry, why bool) {
		if len(entries) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s\n", title)
		for _, e := range entries {
			state := e.Status
			if e.Ongoing {
				state += ", still open"
			}
			fmt.Fprintf(&b, "  %-14s %s  %-22s %s\n", e.IncidentID, e.DetectedAt.UTC().Format("2006-01-02 15:04"), state, rdSpan(e.DurationSeconds))
			if why && e.Why != "" {
				fmt.Fprintf(&b, "                 %s\n", e.Why)
			}
		}
	}
	section("Charged (outages that count against the budget)", l.Charged, false)
	section("Unconfirmed (outages not charged: their real downtime is unknown)", l.Unconfirmed, true)

	var other []string
	for _, g := range []struct {
		name    string
		entries []availability.Entry
	}{{"degraded", l.Degraded}, {"no availability impact", l.None}, {"no declared impact", l.Unclassified}} {
		if len(g.entries) == 0 {
			continue
		}
		var secs int64
		for _, e := range g.entries {
			secs += e.DurationSeconds
		}
		other = append(other, fmt.Sprintf("%d %s (%s)", len(g.entries), g.name, rdSpan(secs)))
	}
	if len(other) > 0 {
		fmt.Fprintf(&b, "\nNot charged: %s\n", strings.Join(other, ", "))
	}
	if len(l.Notes) > 0 {
		b.WriteString("\n")
		for _, n := range l.Notes {
			fmt.Fprintf(&b, "  ! %s\n", n)
		}
	}
	fmt.Fprintf(&b, "\nDuration is %s.\n", l.Basis)
	return b.String()
}

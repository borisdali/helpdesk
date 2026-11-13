package main

// GET /api/v1/availability-ledger?database=<key|name|connection string>: how
// much of a database's downtime budget the real incidents in its window have
// spent (internal/availability). The gateway is where the inputs meet: the
// objectives (infrastructure config), the incidents and the series of the runs
// they spanned (auditd, read by availability.Client, the same code the sysadmin
// agent's restore gate uses), and which playbook series costs availability
// (playbooks.AvailabilityImpact).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// availabilityClient reads incidents from the audit service this gateway uses.
func (g *Gateway) availabilityClient() *availability.Client {
	return &availability.Client{URL: g.auditURL, APIKey: g.auditAPIKey}
}

// availabilityLedger builds the ledger for one database key at a moment.
func (g *Gateway) availabilityLedger(ctx context.Context, key string, now time.Time) (*availability.Ledger, error) {
	res, ok := g.infra.ResolveObjectives(key)
	if !ok {
		return nil, fmt.Errorf("%q is not a configured database", key)
	}
	return g.availabilityClient().Ledger(ctx, key, res.Budget(), now)
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

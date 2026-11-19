package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"helpdesk/internal/audit"
)

type restoreDrillServer struct {
	store      *audit.RestoreDrillStore
	auditStore *audit.Store
}

// handleRecordDrill stores one restore drill and writes its hash-covered
// restore_drill audit event. The table is the query path; the event is the
// authority, and the row carries the event's ID.
// POST /v1/drills
func (s *restoreDrillServer) handleRecordDrill(w http.ResponseWriter, r *http.Request) {
	var d audit.DrillRecord
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if d.DrillID == "" {
		d.DrillID = audit.NewDrillID()
	}
	if d.RecordedAt.IsZero() {
		d.RecordedAt = time.Now().UTC()
	}
	// The event is written before the row, so everything that can reject the
	// record is checked first: no event is left behind for a refused record.
	if err := d.Validate(); err != nil {
		http.Error(w, "invalid drill: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.store.Get(d.DrillID); err == nil {
		http.Error(w, "drill "+d.DrillID+" already exists, records are immutable", http.StatusConflict)
		return
	}
	if d.Supersedes != "" {
		prev, err := s.store.Get(d.Supersedes)
		if errors.Is(err, audit.ErrDrillNotFound) {
			http.Error(w, "supersedes "+d.Supersedes+": drill not found", http.StatusBadRequest)
			return
		} else if err != nil {
			http.Error(w, "failed to look up the superseded drill: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if prev.SourceKey != d.SourceKey {
			http.Error(w, "supersedes "+d.Supersedes+", which is a drill of "+prev.SourceKey+", not "+d.SourceKey, http.StatusBadRequest)
			return
		}
	}

	ev := &audit.Event{
		EventType:   audit.EventTypeRestoreDrill,
		TraceID:     d.DrillID,
		Origin:      "gateway",
		ActionClass: audit.ActionRead,
		RestoreDrill: func() *audit.DrillRecord {
			c := d
			c.AuditEventID = "" // the event cannot carry its own ID
			return &c
		}(),
	}
	if err := s.auditStore.Record(r.Context(), ev); err != nil {
		slog.Error("failed to record restore_drill event", "drill", d.DrillID, "err", err)
		http.Error(w, "failed to record the drill event", http.StatusInternalServerError)
		return
	}
	d.AuditEventID = ev.EventID

	saved, err := s.store.Save(d)
	if err != nil {
		slog.Error("failed to save restore drill", "drill", d.DrillID, "event", ev.EventID, "err", err)
		http.Error(w, "failed to save the drill: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("restore drill recorded", "drill", saved.DrillID, "source", saved.SourceKey,
		"clone", saved.CloneKey, "method", saved.Method, "status", saved.Status, "event", ev.EventID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(saved) //nolint:errcheck
}

// handleListDrills lists drills, newest first.
// GET /v1/drills?source=KEY&system_id=ID&status=S&method=M&since=RFC3339&current=true&limit=N
// With current=true, drills that a later record supersedes are dropped.
func (s *restoreDrillServer) handleListDrills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := audit.DrillQuery{
		SourceKey:      q.Get("source"),
		SourceSystemID: q.Get("system_id"),
		Status:         q.Get("status"),
		Method:         q.Get("method"),
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid since (want RFC3339): "+err.Error(), http.StatusBadRequest)
			return
		}
		opts.Since = t
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	drills, err := s.store.List(opts)
	if err != nil {
		http.Error(w, "failed to list drills: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if q.Get("current") == "true" {
		drills = audit.Current(drills)
	}
	if drills == nil {
		drills = []audit.DrillRecord{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(drills) //nolint:errcheck
}

// handleGetDrill returns one drill.
// GET /v1/drills/{drillID}
func (s *restoreDrillServer) handleGetDrill(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.Get(r.PathValue("drillID"))
	if errors.Is(err, audit.ErrDrillNotFound) {
		http.Error(w, "drill not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to look up the drill: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d) //nolint:errcheck
}

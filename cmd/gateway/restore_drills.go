package main

// Restore drills (v0.36, docs: design in the release notes, operations in
// docs/RESTORE_OPS.md).
//
//	POST /api/v1/drills       record a drill: verify the clone, then store the record
//	GET  /api/v1/drills       list drills (source, status, method, since, current, limit)
//	GET  /api/v1/drills/{id}  one drill
//
// A drill is a restore of the source's backup onto a clone the customer
// brought. Recording one is a human act at the gateway, never the model's: the
// database agent's verify_restore_drill tool (read-only on both servers) reads
// the verdict, this handler adds who and under which ticket, and auditd stores
// the row and its hash-covered restore_drill event. A refused or unverifiable
// drill is stored too, so the trail shows it, but only a verified one counts.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/identity"
	"helpdesk/internal/infra"
)

// drillRequest is the body of POST /api/v1/drills.
type drillRequest struct {
	Source          string    `json:"source"` // db_servers key, name or connection string
	Clone           string    `json:"clone"`  // db_servers key; must be registered
	Method          string    `json:"method"` // pgbackrest | snapshot | cloud | other
	StartedAt       time.Time `json:"started_at,omitempty"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	DurationSeconds int64     `json:"duration_seconds,omitempty"` // used when started_at/finished_at are not both given
	BackupLabel     string    `json:"backup_label,omitempty"`
	RecoveryTarget  string    `json:"recovery_target,omitempty"`
	VerifySQL       string    `json:"verify_sql,omitempty"`
	Ticket          string    `json:"ticket,omitempty"`
	Note            string    `json:"note,omitempty"`
	Supersedes      string    `json:"supersedes,omitempty"`
}

const (
	drillNoteMax  = 1000
	drillFieldMax = 200
)

// validateDrillRequest checks what can be checked without any server. It also
// fixes the duration: from the two times when both are given, else the field.
func validateDrillRequest(req *drillRequest, now time.Time) error {
	req.Source, req.Clone = strings.TrimSpace(req.Source), strings.TrimSpace(req.Clone)
	switch {
	case req.Source == "":
		return fmt.Errorf("source is required: a db_servers key, a name or a connection string")
	case req.Clone == "":
		return fmt.Errorf("clone is required: the db_servers key of the clone")
	}
	switch req.Method {
	case audit.DrillMethodPgBackRest, audit.DrillMethodSnapshot, audit.DrillMethodCloud, audit.DrillMethodOther:
	case "":
		return fmt.Errorf("method is required: pgbackrest, snapshot, cloud or other")
	default:
		return fmt.Errorf("method %q is not one of pgbackrest, snapshot, cloud, other", req.Method)
	}
	if req.Ticket != "" && !recoveryTicketRe.MatchString(req.Ticket) {
		return fmt.Errorf("ticket %q is not a ticket reference (letters, digits and . _ : # / -, up to 64 characters)", req.Ticket)
	}
	if len(req.Note) > drillNoteMax {
		return fmt.Errorf("note is longer than %d characters", drillNoteMax)
	}
	// What is typed here is printed to the terminal of whoever reads the record
	// later, so control characters and bidirectional overrides are refused (a tab
	// and a newline are fine in the free text).
	for name, v := range map[string]string{"source": req.Source, "clone": req.Clone, "backup_label": req.BackupLabel,
		"recovery_target": req.RecoveryTarget, "supersedes": req.Supersedes} {
		if audit.HasUnsafeText(v, false) {
			return fmt.Errorf("%s holds a control character, which is not allowed", name)
		}
	}
	for name, v := range map[string]string{"note": req.Note, "verify_sql": req.VerifySQL} {
		if audit.HasUnsafeText(v, true) {
			return fmt.Errorf("%s holds a control character (other than a tab or newline), which is not allowed", name)
		}
	}
	if req.Supersedes != "" && !drillIDRe.MatchString(req.Supersedes) {
		return fmt.Errorf("supersedes %q is not a drill ID (letters, digits and . _ -, up to 64 characters)", req.Supersedes)
	}
	if len(req.BackupLabel) > drillFieldMax || len(req.RecoveryTarget) > drillFieldMax {
		return fmt.Errorf("backup_label and recovery_target are limited to %d characters", drillFieldMax)
	}
	if req.DurationSeconds < 0 {
		return fmt.Errorf("duration_seconds cannot be negative")
	}
	if !req.FinishedAt.IsZero() && req.FinishedAt.After(now.Add(time.Minute)) {
		return fmt.Errorf("finished_at is in the future")
	}
	if !req.StartedAt.IsZero() && !req.FinishedAt.IsZero() {
		if req.FinishedAt.Before(req.StartedAt) {
			return fmt.Errorf("finished_at is before started_at")
		}
		req.DurationSeconds = int64(req.FinishedAt.Sub(req.StartedAt).Seconds())
	}
	if req.FinishedAt.IsZero() && req.DurationSeconds > 0 && !req.StartedAt.IsZero() {
		req.FinishedAt = req.StartedAt.Add(time.Duration(req.DurationSeconds) * time.Second)
	}
	return nil
}

var drillIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var drillPasswordRe = regexp.MustCompile(`(?i)\bpassword=\S+`)

// drillInfraDigest digests the two infra entries with every secret taken out,
// so a later edit of infra.json is detectable from the record without the
// record holding anything sensitive. Only the name of the password variable is
// included, never its value.
func drillInfraDigest(source, clone infra.DBServer) string {
	strip := func(d infra.DBServer) map[string]any {
		return map[string]any{
			"name":              d.Name,
			"connection_string": drillPasswordRe.ReplaceAllString(d.ConnectionString, "password=***"),
			"password_env":      d.PasswordEnv,
			"k8s_cluster":       d.K8sCluster,
			"k8s_namespace":     d.K8sNamespace,
			"k8s_pod_selector":  d.K8sPodSelector,
			"vm_name":           d.VMName,
			"container_name":    d.ContainerName,
			"systemd_unit":      d.SystemdUnit,
			"environment":       d.Environment,
		}
	}
	b, _ := json.Marshal([]map[string]any{strip(source), strip(clone)}) // map keys marshal sorted
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// drillHostType says where the source runs, for the record.
func drillHostType(d infra.DBServer) string {
	switch {
	case d.K8sCluster != "":
		return "k8s"
	case d.VMName != "" && d.ContainerName != "":
		return "vm-container"
	case d.VMName != "":
		return "vm"
	case d.ContainerName != "":
		return "container"
	case d.SystemdUnit != "":
		return "systemd"
	}
	return "host"
}

// callDirectTool posts to an agent's /tool/{name} and returns the tool's output
// text: the same call dispatchDirectTool makes, for a caller that is not
// answering an HTTP request with the result.
func (g *Gateway) callDirectTool(ctx context.Context, agentName, toolName string, args map[string]any, traceID string) (string, error) {
	agentInfo, ok := g.agents[agentName]
	if !ok {
		return "", fmt.Errorf("agent %q not available", agentName)
	}
	body, err := json.Marshal(directToolReq{TraceID: traceID, Args: args})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(agentInfo.InvokeURL, "/invoke")+"/tool/"+toolName, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.agentAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.agentAPIKey)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("direct tool call to %s: %w", agentName, err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	var tr directToolResp
	if err := json.Unmarshal(respBytes, &tr); err != nil {
		tr.Output = string(respBytes)
	}
	if resp.StatusCode >= 400 || tr.Error != "" {
		text := tr.Error
		if text == "" {
			text = string(respBytes)
		}
		return "", fmt.Errorf("%s returned %d: %s", agentName, resp.StatusCode, text)
	}
	return tr.Output, nil
}

// drillAttempt carries what a failed attempt to record a drill is audited with.
// A recorded drill leaves its own restore_drill event; an attempt that does not
// end in one (rejected, could not be verified, could not be stored) leaves a
// gateway request event instead, so the trail shows who tried what and why it
// did not go through. Failures of authentication and authorization are audited
// by the auth wrapper before the handler runs.
type drillAttempt struct {
	g         *Gateway
	r         *http.Request
	w         http.ResponseWriter
	start     time.Time
	requestID string
	traceID   string
	principal identity.ResolvedPrincipal
	params    map[string]any
	agent     string
}

// fail answers with an error and audits the attempt.
func (a *drillAttempt) fail(code int, msg string) {
	a.g.recordAudit(a.r.Context(), &audit.GatewayRequest{
		RequestID:      a.requestID,
		TraceID:        a.traceID,
		Endpoint:       a.r.URL.Path,
		Method:         a.r.Method,
		Agent:          a.agent,
		ToolName:       "record_restore_drill",
		ToolParameters: a.params,
		// Not classified by name: an unknown tool would default to a class that
		// can attach an approval requirement, and a failed attempt to record a
		// drill touches no database.
		ActionClass:       audit.ActionRead,
		StartTime:         a.start,
		Duration:          time.Since(a.start),
		Status:            "error",
		Error:             msg,
		HTTPCode:          code,
		Principal:         a.principal.EffectiveID(),
		ResolvedPrincipal: a.principal,
	})
	writeError(a.w, code, msg)
}

// drillParams is the request as the audit event records it: who the drill is
// for and what was claimed, with the free text bounded.
func drillParams(req drillRequest) map[string]any {
	clip := func(s string, n int) string {
		if len(s) > n {
			return s[:n] + "…"
		}
		return s
	}
	p := map[string]any{"source": req.Source, "clone": req.Clone, "method": req.Method}
	for k, v := range map[string]string{"ticket": req.Ticket, "supersedes": req.Supersedes,
		"backup_label": req.BackupLabel, "verify_sql": clip(req.VerifySQL, 200)} {
		if v != "" {
			p[k] = v
		}
	}
	return p
}

func (g *Gateway) handleRecordDrill(w http.ResponseWriter, r *http.Request) {
	traceID := r.Header.Get("X-Trace-ID")
	if traceID == "" {
		traceID = audit.NewTraceIDWithPrefix("drl_")
	}
	w.Header().Set("X-Trace-ID", traceID)
	principal, _, _, _, err := g.resolveRequest(r, "", "")
	att := &drillAttempt{g: g, r: r, w: w, start: time.Now(), requestID: uuid.New().String()[:8],
		traceID: traceID, principal: principal, agent: "gateway", params: map[string]any{}}
	if err != nil {
		att.fail(http.StatusUnauthorized, "authentication failed: "+err.Error())
		return
	}
	if g.auditURL == "" {
		att.fail(http.StatusServiceUnavailable, "no audit service is configured; a drill has to be recorded in it")
		return
	}
	if g.infra == nil {
		att.fail(http.StatusNotFound, "no infrastructure configuration is loaded; both the source and the clone must be registered in it")
		return
	}
	var req drillRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		att.fail(http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	att.params = drillParams(req)
	now := time.Now().UTC()
	if err := validateDrillRequest(&req, now); err != nil {
		att.fail(http.StatusBadRequest, err.Error())
		return
	}

	srcKey, err := g.resolveDrillTarget("source", req.Source)
	if err != nil {
		att.fail(drillResolveStatus(err), err.Error())
		return
	}
	cloneKey, err := g.resolveDrillTarget("clone", req.Clone)
	if err != nil {
		att.fail(drillResolveStatus(err), err.Error())
		return
	}
	if srcKey == cloneKey {
		att.fail(http.StatusBadRequest, fmt.Sprintf("the clone cannot be the source (%s)", srcKey))
		return
	}

	args := map[string]any{"source": srcKey, "clone": cloneKey}
	if req.VerifySQL != "" {
		args["verify_sql"] = req.VerifySQL
	}
	att.agent = agentNameDB
	out, err := g.callDirectTool(r.Context(), agentNameDB, "verify_restore_drill", args, traceID)
	if err != nil {
		att.fail(http.StatusBadGateway, "could not verify the clone: "+err.Error())
		return
	}
	var ver audit.DrillVerification
	if err := json.Unmarshal([]byte(out), &ver); err != nil || ver.Status == "" {
		// No verdict: the tool refused the request itself (an unusable verify_sql,
		// an unregistered database). Nothing is recorded.
		text := ver.Output
		if text == "" {
			text = strings.TrimSpace(out)
		}
		att.fail(http.StatusUnprocessableEntity, text)
		return
	}

	// With no identity provider configured nobody is identified; the record says
	// so instead of leaving who blank.
	recordedBy := principal.EffectiveID()
	if recordedBy == "" {
		recordedBy = "anonymous"
	}
	srcEntry, cloneEntry := g.infra.DBServers[srcKey], g.infra.DBServers[cloneKey]
	rec := audit.DrillRecord{
		SourceKey: srcKey, CloneKey: cloneKey,
		SourceSystemID: ver.SourceSystemID, CloneSystemID: ver.CloneSystemID,
		SourceEndpoint: ver.SourceEndpoint, CloneEndpoint: ver.CloneEndpoint,
		SourceHostType: drillHostType(srcEntry),
		InfraDigest:    drillInfraDigest(srcEntry, cloneEntry),
		Method:         req.Method,
		StartedAt:      req.StartedAt, FinishedAt: req.FinishedAt,
		DurationSeconds: req.DurationSeconds,
		// The figure is the operator's word. Only a drill aiHelpDesk timed itself
		// is "observed", and that is not recorded through this endpoint.
		DurationSource: audit.DrillDurationReported,
		// The size is what the verification measured on the clone, not a claim.
		RestoreBytes:   ver.CloneBytes,
		BackupLabel:    req.BackupLabel,
		RecoveryTarget: req.RecoveryTarget,
		Status:         ver.Status,
		Checks:         ver.Checks,
		Supersedes:     req.Supersedes,
		RecordedBy:     recordedBy,
		RecordedAt:     now,
		Ticket:         req.Ticket,
		Note:           req.Note,
	}
	if req.DurationSeconds == 0 {
		rec.DurationSource = ""
	}

	saved, status, err := g.postDrill(r.Context(), rec)
	if err != nil {
		att.agent = "gateway"
		att.fail(status, err.Error())
		return
	}
	slog.Info("restore drill recorded", "drill", saved.DrillID, "source", srcKey, "clone", cloneKey,
		"status", saved.Status, "by", recordedBy, "ticket", rec.Ticket)
	writeJSON(w, http.StatusCreated, map[string]any{"drill": saved, "verification": ver.Output})
}

func (g *Gateway) resolveDrillTarget(role, target string) (string, error) {
	key, err := g.infra.ResolveTarget(target)
	if err == nil {
		return key, nil
	}
	var amb *infra.AmbiguousTargetError
	if errors.As(err, &amb) {
		return "", fmt.Errorf("the %s %q is ambiguous: %w", role, target, err)
	}
	hint := ""
	if role == "clone" {
		hint = "; the clone has to be registered in the infrastructure config before a drill can be recorded against it"
	}
	return "", fmt.Errorf("the %s %q is not in the infrastructure configuration%s", role, target, hint)
}

func drillResolveStatus(err error) int {
	if strings.Contains(err.Error(), "ambiguous") {
		return http.StatusConflict
	}
	return http.StatusNotFound
}

// postDrill stores the record in auditd and returns what auditd saved.
func (g *Gateway) postDrill(ctx context.Context, rec audit.DrillRecord) (audit.DrillRecord, int, error) {
	body, _ := json.Marshal(rec)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(g.auditURL, "/")+"/v1/drills", bytes.NewReader(body))
	if err != nil {
		return audit.DrillRecord{}, http.StatusInternalServerError, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return audit.DrillRecord{}, http.StatusBadGateway, fmt.Errorf("could not reach the audit service: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		status := resp.StatusCode
		if status >= 500 {
			status = http.StatusBadGateway
		}
		return audit.DrillRecord{}, status, fmt.Errorf("the audit service did not record the drill (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var saved audit.DrillRecord
	if err := json.Unmarshal(data, &saved); err != nil {
		return audit.DrillRecord{}, http.StatusBadGateway, fmt.Errorf("unreadable answer from the audit service: %w", err)
	}
	return saved, http.StatusCreated, nil
}

// handleListDrills passes the filters to auditd. source may be any way of
// naming the database; it is resolved to the key the records are filed under.
func (g *Gateway) handleListDrills(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "no audit service is configured")
		return
	}
	q := r.URL.Query()
	out := url.Values{}
	if s := strings.TrimSpace(q.Get("source")); s != "" {
		key := s
		if g.infra != nil {
			if k, err := g.infra.ResolveTarget(s); err == nil {
				key = k
			}
		}
		out.Set("source", key)
	}
	for _, k := range []string{"status", "method", "since", "current", "limit", "system_id"} {
		if v := q.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	g.proxyAuditGet(w, r, "/v1/drills?"+out.Encode())
}

func (g *Gateway) handleGetDrill(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "no audit service is configured")
		return
	}
	g.proxyAuditGet(w, r, "/v1/drills/"+url.PathEscape(r.PathValue("id")))
}

func (g *Gateway) proxyAuditGet(w http.ResponseWriter, r *http.Request, path string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimSuffix(g.auditURL, "/")+path, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not reach the audit service: "+err.Error())
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("the audit service failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data))))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(data) //nolint:errcheck
}

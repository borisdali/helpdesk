package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// ── fixtures ──────────────────────────────────────────────────────────────────

// plainApprovalServer is an approvalServer on a real store with no identity
// provider (legacy mode), called directly so path values are set by the test.
func plainApprovalServer(t *testing.T, n *ApprovalNotifier) *approvalServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	as, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatal(err)
	}
	return &approvalServer{store: as, notifier: n}
}

func call(h http.HandlerFunc, method, target string, body any, pathID string) *httptest.ResponseRecorder {
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		rdr = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, rdr)
	if pathID != "" {
		req.SetPathValue("approvalID", pathID)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func createApproval(t *testing.T, s *approvalServer, body map[string]any) string {
	t.Helper()
	rec := call(s.handleCreateApproval, http.MethodPost, "/v1/approvals", body, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil || out.ApprovalID == "" {
		t.Fatalf("create response: %v / %s", err, rec.Body.String())
	}
	return out.ApprovalID
}

// webhookSink is an HTTP endpoint that records what it is sent.
type webhookSink struct {
	mu     sync.Mutex
	bodies []map[string]any
	got    chan struct{}
	status int
}

func newWebhookSink(t *testing.T, path string, status int) (*webhookSink, string) {
	t.Helper()
	s := &webhookSink{got: make(chan struct{}, 16), status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		s.mu.Lock()
		s.bodies = append(s.bodies, m)
		s.mu.Unlock()
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		s.got <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL + path
}

func (s *webhookSink) wait(t *testing.T) map[string]any {
	t.Helper()
	select {
	case <-s.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no notification arrived")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[len(s.bodies)-1]
}

func (s *webhookSink) none(t *testing.T) {
	t.Helper()
	select {
	case <-s.got:
		t.Fatal("a notification arrived that should not have")
	case <-time.After(200 * time.Millisecond):
	}
}

// fakeSMTP is a minimal SMTP server: it accepts one message at a time and hands
// the DATA to a channel. It advertises AUTH PLAIN and nothing else.
type fakeSMTP struct {
	addr string
	msgs chan string
	from chan string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{addr: ln.Addr().String(), msgs: make(chan string, 8), from: make(chan string, 8)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	reply := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			reply("250-fake")
			reply("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			reply("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			f.from <- strings.TrimSpace(line)
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go")
			var msg strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				msg.WriteString(l)
			}
			f.msgs <- msg.String()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (f *fakeSMTP) message(t *testing.T) string {
	t.Helper()
	select {
	case m := <-f.msgs:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no email arrived")
	}
	return ""
}

func (f *fakeSMTP) hostPort() (string, string) {
	h, p, _ := net.SplitHostPort(f.addr)
	return h, p
}

func baseApproval(id, status string) *audit.StoredApproval {
	return &audit.StoredApproval{
		ApprovalID: id, Status: status, ActionClass: "write", ToolName: "restore_to_point_in_time", AgentName: "sysadmin",
		ResourceType: "database", ResourceName: "prod-db", RequestedBy: "alice",
		RequestedAt: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC),
	}
}

// ── the HTTP handlers ─────────────────────────────────────────────────────────

func TestHandleCreateApproval_Validation(t *testing.T) {
	s := plainApprovalServer(t, nil)
	for _, tc := range []struct {
		name string
		body any
		want string
	}{
		{"not JSON", "{nope", "invalid JSON"},
		{"no action class", map[string]any{"requested_by": "alice"}, "action_class is required"},
		{"no requester", map[string]any{"action_class": "write"}, "requested_by is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(s.handleCreateApproval, http.MethodPost, "/v1/approvals", tc.body, "")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleCreateApproval_StoresEverythingAndDefaultsTheExpiry(t *testing.T) {
	s := plainApprovalServer(t, nil)
	before := time.Now().UTC()
	id := createApproval(t, s, map[string]any{
		"action_class": "destructive", "tool_name": "restore_to_point_in_time", "agent_name": "sysadmin",
		"resource_type": "database", "resource_name": "prod-db", "requested_by": "alice", "policy_name": "p1",
		"approver_role": "dba", "trace_id": "tr_1", "event_id": "ev_1", "callback_url": "http://cb.example/x",
		"request_context": map[string]any{"why": "a dropped table"},
	})
	got, err := s.store.GetRequest(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.ActionClass != "destructive" || got.ToolName != "restore_to_point_in_time" ||
		got.AgentName != "sysadmin" || got.ResourceName != "prod-db" || got.RequestedBy != "alice" ||
		got.PolicyName != "p1" || got.ApproverRole != "dba" || got.TraceID != "tr_1" || got.CallbackURL != "http://cb.example/x" {
		t.Errorf("stored = %+v", got)
	}
	if got.RequestContext["why"] != "a dropped table" {
		t.Errorf("context = %v", got.RequestContext)
	}
	// No expiry asked for: an hour.
	if d := got.ExpiresAt.Sub(before); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("default expiry in %v, want about 60m", d)
	}
	// One asked for: that.
	id2 := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice", "expires_in_minutes": 5})
	got2, _ := s.store.GetRequest(context.Background(), id2)
	if d := got2.ExpiresAt.Sub(before); d < 4*time.Minute || d > 6*time.Minute {
		t.Errorf("expiry in %v, want about 5m", d)
	}
}

func TestHandleCreateApproval_NotifiesAndRegistersTheCallback(t *testing.T) {
	hook, hookURL := newWebhookSink(t, "/hook", 0)
	n := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: hookURL})
	s := plainApprovalServer(t, n)
	id := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice", "callback_url": "http://cb.example/x", "tool_name": "t"})
	body := hook.wait(t)
	if body["event_type"] != "approval_created" || body["approval_id"] != id || body["requested_by"] != "alice" || body["status"] != "pending" {
		t.Errorf("webhook = %v", body)
	}
	if n.callbackURLs[id] != "http://cb.example/x" {
		t.Errorf("callback not registered: %v", n.callbackURLs)
	}
}

func TestHandleGetApproval(t *testing.T) {
	s := plainApprovalServer(t, nil)
	id := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
	rec := call(s.handleGetApproval, http.MethodGet, "/v1/approvals/"+id, nil, id)
	var got audit.StoredApproval
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.ApprovalID != id || got.Status != "pending" {
		t.Errorf("%d %+v", rec.Code, got)
	}
	if rec := call(s.handleGetApproval, http.MethodGet, "/v1/approvals/", nil, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := call(s.handleGetApproval, http.MethodGet, "/v1/approvals/nope", nil, "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}
}

func TestHandleListApprovals_Filters(t *testing.T) {
	s := plainApprovalServer(t, nil)
	a := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice", "agent_name": "sysadmin", "tool_name": "restore", "trace_id": "tr_a"})
	createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "bob", "agent_name": "database", "tool_name": "vacuum", "trace_id": "tr_b"})
	createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "bob", "agent_name": "database", "tool_name": "vacuum", "trace_id": "tr_c"})
	if err := s.store.Deny(context.Background(), a, "carol", "no"); err != nil {
		t.Fatal(err)
	}
	count := func(query string) int {
		t.Helper()
		rec := call(s.handleListApprovals, http.MethodGet, "/v1/approvals"+query, nil, "")
		var out []audit.StoredApproval
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		return len(out)
	}
	for query, want := range map[string]int{
		"": 3, "?status=pending": 2, "?status=denied": 1, "?agent=database": 2, "?agent=sysadmin": 1, "?trace_id=tr_b": 1,
		"?requested_by=bob": 2, "?tool_name=vacuum": 2, "?limit=1": 1,
		"?limit=banana": 3, // an unreadable limit is ignored
		"?limit=-4":     3,
	} {
		if got := count(query); got != want {
			t.Errorf("list%s = %d, want %d", query, got, want)
		}
	}
}

func TestHandlePendingApprovals_OnlyPending(t *testing.T) {
	s := plainApprovalServer(t, nil)
	a := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
	createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
	if err := s.store.Approve(context.Background(), a, "carol", "ok", 0); err != nil {
		t.Fatal(err)
	}
	rec := call(s.handlePendingApprovals, http.MethodGet, "/v1/approvals/pending", nil, "")
	var out []audit.StoredApproval
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&out) != nil || len(out) != 1 || out[0].ApprovalID == a {
		t.Errorf("%d %+v", rec.Code, out)
	}
}

func TestHandleCancel(t *testing.T) {
	hook, hookURL := newWebhookSink(t, "/hook", 0)
	n := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: hookURL})
	s := plainApprovalServer(t, n)
	id := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
	hook.wait(t) // the "created" notification

	rec := call(s.handleCancel, http.MethodPost, "/v1/approvals/"+id+"/cancel", map[string]any{"reason": "changed my mind"}, id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cancelled") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.store.GetRequest(context.Background(), id)
	if got.Status != "cancelled" {
		t.Errorf("status = %s", got.Status)
	}
	// Anonymous cancel is attributed to the system, and the resolution is announced.
	if body := hook.wait(t); body["event_type"] != "approval_resolved" || body["status"] != "cancelled" {
		t.Errorf("resolved notification = %v", body)
	}
	if got.ResolvedBy != "system" {
		t.Errorf("cancelled by %q, want system", got.ResolvedBy)
	}
	// It cannot be cancelled twice, and cancelling nothing is an error.
	if rec := call(s.handleCancel, http.MethodPost, "/x", nil, id); rec.Code != http.StatusBadRequest {
		t.Errorf("second cancel: %d", rec.Code)
	}
	if rec := call(s.handleCancel, http.MethodPost, "/x", nil, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := call(s.handleCancel, http.MethodPost, "/x", "{nope", "someid"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Errorf("bad body: %d %s", rec.Code, rec.Body.String())
	}
	// A named canceller is kept.
	id2 := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
	call(s.handleCancel, http.MethodPost, "/x", map[string]any{"cancelled_by": "dave"}, id2)
	if got, _ := s.store.GetRequest(context.Background(), id2); got.ResolvedBy != "dave" {
		t.Errorf("cancelled by %q, want dave", got.ResolvedBy)
	}
}

func TestHandleWaitForApproval(t *testing.T) {
	s := plainApprovalServer(t, nil)

	t.Run("a decision that arrives while waiting is returned", func(t *testing.T) {
		id := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
		go func() {
			time.Sleep(150 * time.Millisecond)
			_ = s.store.Approve(context.Background(), id, "carol", "ok", 0)
		}()
		rec := call(s.handleWaitForApproval, http.MethodGet, "/v1/approvals/"+id+"/wait?timeout=5s", nil, id)
		var got audit.StoredApproval
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.Status != "approved" || got.ResolvedBy != "carol" {
			t.Errorf("%d %+v", rec.Code, got)
		}
	})
	t.Run("no decision in time returns the approval as it stands", func(t *testing.T) {
		id := createApproval(t, s, map[string]any{"action_class": "write", "requested_by": "alice"})
		start := time.Now()
		rec := call(s.handleWaitForApproval, http.MethodGet, "/v1/approvals/"+id+"/wait?timeout=100ms", nil, id)
		var got audit.StoredApproval
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.Status != "pending" {
			t.Errorf("%d %+v", rec.Code, got)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("waited %v for a 100ms timeout", time.Since(start))
		}
	})
	t.Run("an approval that does not exist", func(t *testing.T) {
		if rec := call(s.handleWaitForApproval, http.MethodGet, "/x?timeout=100ms", nil, "nope"); rec.Code != http.StatusNotFound {
			t.Errorf("unknown id: %d", rec.Code)
		}
		if rec := call(s.handleWaitForApproval, http.MethodGet, "/x", nil, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("no id: %d", rec.Code)
		}
	})
}

// The expiration worker stops when it is told to; it does not leak.
func TestStartExpirationWorker_StopsWithItsContext(t *testing.T) {
	s := plainApprovalServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.startExpirationWorker(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the expiration worker did not stop")
	}
}

// ── the notifier ──────────────────────────────────────────────────────────────

func TestNewApprovalNotifier_ReadsItsConfig(t *testing.T) {
	n := NewApprovalNotifier(ApprovalNotifierConfig{BaseURL: "http://gw:1199/", EmailTo: " a@x.com , b@x.com ,c@x.com", SMTPHost: "h", SMTPPort: "25"})
	if n.baseURL != "http://gw:1199" {
		t.Errorf("baseURL = %q", n.baseURL)
	}
	if got := strings.Join(n.emailTo, "|"); got != "a@x.com|b@x.com|c@x.com" {
		t.Errorf("emailTo = %q", got)
	}
	for _, tc := range []struct {
		name string
		cfg  ApprovalNotifierConfig
		want bool
	}{
		{"nothing", ApprovalNotifierConfig{}, false},
		{"a webhook", ApprovalNotifierConfig{WebhookURL: "http://x"}, true},
		{"a mail host with no recipient", ApprovalNotifierConfig{SMTPHost: "h"}, false},
		{"recipients with no mail host", ApprovalNotifierConfig{EmailTo: "a@x.com"}, false},
		{"a mail host and a recipient", ApprovalNotifierConfig{SMTPHost: "h", EmailTo: "a@x.com"}, true},
	} {
		if got := NewApprovalNotifier(tc.cfg).IsEnabled(); got != tc.want {
			t.Errorf("%s: IsEnabled = %v, want %v", tc.name, got, tc.want)
		}
	}
	m := NewApprovalNotifier(ApprovalNotifierConfig{})
	m.RegisterCallback("a1", "")
	m.RegisterCallback("a2", "http://cb")
	if len(m.callbackURLs) != 1 || m.callbackURLs["a2"] != "http://cb" {
		t.Errorf("callbacks = %v", m.callbackURLs)
	}
}

func TestNotifyCreated_DisabledSendsNothingAndRegistersNothing(t *testing.T) {
	n := NewApprovalNotifier(ApprovalNotifierConfig{})
	a := baseApproval("apr_1", "pending")
	a.CallbackURL = "http://cb"
	n.NotifyCreated(context.Background(), a)
	if len(n.callbackURLs) != 0 {
		t.Errorf("a disabled notifier registered %v", n.callbackURLs)
	}
}

func TestNotifyResolved_CallsBackOnceAndWebhooks(t *testing.T) {
	cb, cbURL := newWebhookSink(t, "/cb", 0)
	hook, hookURL := newWebhookSink(t, "/hook", 0)
	n := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: hookURL})
	n.RegisterCallback("apr_1", cbURL)

	a := baseApproval("apr_1", "approved")
	a.ResolvedBy, a.ResolvedAt, a.ResolutionReason = "carol", time.Date(2026, 11, 9, 10, 5, 0, 0, time.UTC), "looks right"
	a.ApprovalValidUntil = time.Date(2026, 11, 9, 11, 5, 0, 0, time.UTC)
	n.NotifyResolved(context.Background(), a)

	call := cb.wait(t)
	if call["approval_id"] != "apr_1" || call["status"] != "approved" || call["resolved_by"] != "carol" ||
		call["resolution_reason"] != "looks right" || call["valid_until"] != "2026-11-09T11:05:00Z" {
		t.Errorf("callback = %v", call)
	}
	h := hook.wait(t)
	if h["event_type"] != "approval_resolved" || h["resolved_by"] != "carol" || h["reason"] != "looks right" || h["expires_at"] != "2026-11-09T11:00:00Z" ||
		h["resource"] != "database/prod-db" {
		t.Errorf("webhook = %v", h)
	}
	// The callback is spent: a second resolution does not call it again.
	if _, still := n.callbackURLs["apr_1"]; still {
		t.Error("the callback URL was kept after use")
	}
	n.NotifyResolved(context.Background(), a)
	hook.wait(t)
	cb.none(t)
}

func TestSendCallback_OmitsValidUntilWhenThereIsNone(t *testing.T) {
	cb, cbURL := newWebhookSink(t, "/cb", 0)
	n := NewApprovalNotifier(ApprovalNotifierConfig{})
	a := baseApproval("apr_2", "denied")
	a.ResolvedAt = time.Now()
	n.sendCallback(cbURL, a)
	if body := cb.wait(t); body["status"] != "denied" || body["valid_until"] != nil {
		t.Errorf("callback = %v", body)
	}
}

// A webhook that is down, or answers with an error, is logged and not fatal.
func TestNotifier_SurvivesAFailingEndpoint(t *testing.T) {
	_, errURL := newWebhookSink(t, "/hook", http.StatusInternalServerError)
	n := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: errURL})
	a := baseApproval("apr_3", "pending")
	n.sendWebhook(a, "created")
	n.sendCallback(errURL, a)
	dead := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: "http://127.0.0.1:1/hook"})
	dead.sendWebhook(a, "created")
	dead.sendCallback("http://127.0.0.1:1/cb", a)
}

// A Slack incoming webhook gets Slack's own shape, titled and coloured by status.
func TestSendWebhook_SlackFormat(t *testing.T) {
	for _, tc := range []struct{ status, title, color string }{
		{"pending", "Approval Request Created", "#FFA500"},
		{"approved", "Approval Granted", "#36A64F"},
		{"denied", "Approval Denied", "#FF0000"},
		{"expired", "Approval Expired", "#808080"},
		{"cancelled", "Approval Cancelled", "#808080"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			hook, url := newWebhookSink(t, "/slack.com/services/T/B/x", 0)
			n := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: url})
			a := baseApproval("apr_4", tc.status)
			a.ResolvedBy, a.ResolutionReason = "carol", "because"
			if tc.status == "pending" {
				a.ResolvedBy, a.ToolName, a.AgentName = "", "", ""
			}
			n.sendWebhook(a, "resolved")
			body := hook.wait(t)
			atts, _ := body["attachments"].([]any)
			if len(atts) != 1 {
				t.Fatalf("body = %v", body)
			}
			att := atts[0].(map[string]any)
			text, _ := att["text"].(string)
			if att["color"] != tc.color || !strings.Contains(text, tc.title) || !strings.Contains(text, "`apr_4`") || !strings.Contains(text, "*Requested by:* alice") {
				t.Errorf("attachment = %v", att)
			}
			if tc.status != "pending" && (!strings.Contains(text, "*Resolved by:* carol") || !strings.Contains(text, "*Reason:* because")) {
				t.Errorf("a resolved approval's text lacks who and why: %q", text)
			}
			if tc.status == "pending" && (strings.Contains(text, "*Tool:*") || strings.Contains(text, "*Agent:*") || strings.Contains(text, "Resolved by")) {
				t.Errorf("empty fields were printed: %q", text)
			}
		})
	}
}

func TestSendEmail(t *testing.T) {
	smtpSrv := newFakeSMTP(t)
	host, port := smtpSrv.hostPort()
	cfg := ApprovalNotifierConfig{SMTPHost: host, SMTPPort: port, EmailFrom: "helpdesk@example.com", EmailTo: "ops@example.com, dba@example.com",
		BaseURL: "http://gw:1199/", SMTPUser: "u", SMTPPassword: "p"}
	n := NewApprovalNotifier(cfg)
	a := baseApproval("apr_5", "pending")

	n.sendEmail(a, "created")
	msg := smtpSrv.message(t)
	for _, want := range []string{
		"From: helpdesk@example.com", "To: ops@example.com,dba@example.com",
		"Subject: [APPROVAL REQUIRED] write - restore_to_point_in_time",
		"Approval ID: apr_5", "Requested:   2026-11-09T10:00:00Z by alice",
		`curl -X POST "http://gw:1199/v1/approvals/apr_5/approve"`, `curl -X POST "http://gw:1199/v1/approvals/apr_5/deny"`,
		`approvals approve apr_5 --reason`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the email lacks %q:\n%s", want, msg)
		}
	}

	// Without a base URL there are no links, only the CLI commands.
	cfg.BaseURL = ""
	NewApprovalNotifier(cfg).sendEmail(a, "created")
	if msg := smtpSrv.message(t); strings.Contains(msg, "curl -X POST") || !strings.Contains(msg, "approvals deny apr_5") {
		t.Errorf("email with no base URL:\n%s", msg)
	}

	// A resolution reads differently.
	d := baseApproval("apr_6", "denied")
	d.ResolvedBy, d.ResolvedAt, d.ResolutionReason = "carol", time.Date(2026, 11, 9, 10, 9, 0, 0, time.UTC), "wrong target"
	n.sendEmail(d, "resolved")
	msg = smtpSrv.message(t)
	for _, want := range []string{"Subject: [APPROVAL DENIED] write - restore_to_point_in_time", "Resolved:    2026-11-09T10:09:00Z by carol", "Reason:      wrong target"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the denial email lacks %q:\n%s", want, msg)
		}
	}

	// No mail server: logged, not fatal.
	NewApprovalNotifier(ApprovalNotifierConfig{SMTPHost: "127.0.0.1", SMTPPort: "1", EmailFrom: "a@x", EmailTo: "b@x"}).sendEmail(a, "created")
}

// Creation emails everyone; a resolution emails only when it is a denial.
func TestNotifier_EmailsCreationsAndDenialsOnly(t *testing.T) {
	smtpSrv := newFakeSMTP(t)
	host, port := smtpSrv.hostPort()
	n := NewApprovalNotifier(ApprovalNotifierConfig{SMTPHost: host, SMTPPort: port, EmailFrom: "h@x", EmailTo: "ops@x"})

	n.NotifyCreated(context.Background(), baseApproval("apr_7", "pending"))
	if msg := smtpSrv.message(t); !strings.Contains(msg, "[APPROVAL REQUIRED]") {
		t.Errorf("created:\n%s", msg)
	}
	ok := baseApproval("apr_7", "approved")
	ok.ResolvedAt = time.Now()
	n.NotifyResolved(context.Background(), ok)
	select {
	case m := <-smtpSrv.msgs:
		t.Fatalf("an approval was emailed:\n%s", m)
	case <-time.After(300 * time.Millisecond):
	}
	denied := baseApproval("apr_8", "denied")
	denied.ResolvedAt = time.Now()
	n.NotifyResolved(context.Background(), denied)
	if msg := smtpSrv.message(t); !strings.Contains(msg, "[APPROVAL DENIED]") {
		t.Errorf("denied:\n%s", msg)
	}
}

// The handlers run concurrently, and each registers or spends a callback. This
// is the test the race detector is for (go test -race).
func TestNotifier_ConcurrentCallbacksAreSafe(t *testing.T) {
	n := NewApprovalNotifier(ApprovalNotifierConfig{WebhookURL: "http://127.0.0.1:1/hook"})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("apr_%d", i)
			created := baseApproval(id, "pending")
			created.CallbackURL = "http://127.0.0.1:1/cb"
			n.NotifyCreated(context.Background(), created)
			// A resolution is a fresh record from the store, as in the handlers, not
			// the created one edited while its notification is still being sent.
			resolved := baseApproval(id, "approved")
			resolved.ResolvedAt = time.Now()
			n.NotifyResolved(context.Background(), resolved)
		}(i)
	}
	wg.Wait()
}

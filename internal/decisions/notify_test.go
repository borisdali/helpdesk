package decisions

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func pendingGate() Decision {
	return Decision{
		ID:          "gate:plr_abc123",
		Type:        DecisionTypeGate,
		Status:      "pending",
		Summary:     "Triage complete — ESCALATE_TO pbs_vacuum_remediate",
		RequestedBy: "alice",
		RequestedAt: time.Now(),
		ResolveURL:  "https://helpdesk.internal/api/v1/decisions/gate:plr_abc123/resolve",
		Extra:       map[string]any{"escalation_target": "pbs_vacuum_remediate"},
	}
}

// waitForWebhook blocks until the channel receives one value or the test times out.
func waitForWebhook(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook not received within 3 seconds")
	}
}

func TestDecisionNotifier_NilReceiver_NoOp(t *testing.T) {
	var n *DecisionNotifier
	// Neither call should panic.
	n.NotifyPending(context.Background(), pendingGate())
	n.NotifyResolved(context.Background(), pendingGate())
}

func TestDecisionNotifier_Webhook_Pending(t *testing.T) {
	called := make(chan struct{}, 1)
	var got webhookPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got) //nolint:errcheck
		called <- struct{}{}
	}))
	defer srv.Close()

	n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv.URL})
	n.NotifyPending(context.Background(), pendingGate())
	waitForWebhook(t, called)

	if got.Event != "decision_pending" {
		t.Errorf("event = %q, want decision_pending", got.Event)
	}
	if got.DecisionID != "gate:plr_abc123" {
		t.Errorf("decision_id = %q, want gate:plr_abc123", got.DecisionID)
	}
	if got.Type != DecisionTypeGate {
		t.Errorf("type = %q, want gate", got.Type)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestDecisionNotifier_Webhook_Resolved(t *testing.T) {
	called := make(chan struct{}, 1)
	var got webhookPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got) //nolint:errcheck
		called <- struct{}{}
	}))
	defer srv.Close()

	d := pendingGate()
	d.Status = "approved"
	n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv.URL})
	n.NotifyResolved(context.Background(), d)
	waitForWebhook(t, called)

	if got.Event != "decision_resolved" {
		t.Errorf("event = %q, want decision_resolved", got.Event)
	}
	if got.Status != "approved" {
		t.Errorf("status = %q, want approved", got.Status)
	}
}

func TestDecisionNotifier_HMACSigning(t *testing.T) {
	const secret = "test-secret"
	called := make(chan struct{}, 1)
	var gotSig string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Helpdesk-Signature")
		gotBody, _ = io_ReadAll(r.Body)
		called <- struct{}{}
	}))
	defer srv.Close()

	n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv.URL, WebhookSecret: secret})
	n.NotifyPending(context.Background(), pendingGate())
	waitForWebhook(t, called)

	if !strings.HasPrefix(gotSig, "sha256=") {
		t.Fatalf("X-Helpdesk-Signature = %q, want sha256= prefix", gotSig)
	}
	hexPart := strings.TrimPrefix(gotSig, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	want := hex.EncodeToString(mac.Sum(nil))
	if hexPart != want {
		t.Errorf("HMAC mismatch: got %s, want %s", hexPart, want)
	}
}

func TestDecisionNotifier_NoHMACWhenSecretEmpty(t *testing.T) {
	called := make(chan struct{}, 1)
	var gotSig string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Helpdesk-Signature")
		called <- struct{}{}
	}))
	defer srv.Close()

	n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv.URL}) // no secret
	n.NotifyPending(context.Background(), pendingGate())
	waitForWebhook(t, called)

	if gotSig != "" {
		t.Errorf("expected no X-Helpdesk-Signature, got %q", gotSig)
	}
}

func TestDecisionNotifier_SlackDetection_Pending(t *testing.T) {
	called := make(chan struct{}, 1)
	var body []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io_ReadAll(r.Body)
		called <- struct{}{}
	}))
	defer srv.Close()

	// Replace the host with one containing "slack.com" by using a redirect trick.
	// We can't use a real Slack URL in tests, so we patch the URL manually.
	slackURL := strings.Replace(srv.URL, "127.0.0.1", "slack.com.127.0.0.1", 1)
	// That won't resolve — use the real srv.URL but construct a notifier whose
	// WebhookURL contains "slack.com" as a substring while still hitting our server.
	// Simplest: use a fake host and override the httpClient transport.
	called2 := make(chan struct{}, 1)
	var slackBody []byte
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackBody, _ = io_ReadAll(r.Body)
		called2 <- struct{}{}
	}))
	defer srv2.Close()

	n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv2.URL + "/slack.com/services/fake"})
	n.NotifyPending(context.Background(), pendingGate())
	waitForWebhook(t, called2)

	_ = slackURL
	_ = body

	// Slack payload must have an "attachments" key, not "event".
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(slackBody, &parsed); err != nil {
		t.Fatalf("unmarshal slack body: %v", err)
	}
	if _, ok := parsed["attachments"]; !ok {
		t.Errorf("slack payload missing 'attachments' key; got keys: %v", mapKeys(parsed))
	}
	if _, ok := parsed["event"]; ok {
		t.Errorf("slack payload must not have 'event' key (got raw JSON payload instead of Slack format)")
	}
}

func TestDecisionNotifier_SlackColors(t *testing.T) {
	cases := []struct {
		status    string
		wantColor string
	}{
		{"pending", "#f0ad4e"},
		{"approved", "#36a64f"},
		{"denied", "#d00000"},
		{"abandoned", "#d00000"},
		{"expired", "#d00000"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.status, func(t *testing.T) {
			called := make(chan struct{}, 1)
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io_ReadAll(r.Body)
				called <- struct{}{}
			}))
			defer srv.Close()

			n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv.URL + "/slack.com/fake"})
			d := pendingGate()
			d.Status = tc.status
			if tc.status != "pending" {
				n.NotifyResolved(context.Background(), d)
			} else {
				n.NotifyPending(context.Background(), d)
			}
			waitForWebhook(t, called)

			var msg slackMessage
			if err := json.Unmarshal(body, &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(msg.Attachments) == 0 {
				t.Fatal("no attachments")
			}
			if msg.Attachments[0].Color != tc.wantColor {
				t.Errorf("color = %q, want %q", msg.Attachments[0].Color, tc.wantColor)
			}
		})
	}
}

func TestDecisionNotifier_NonBlocking(t *testing.T) {
	// Notifier should return immediately even if the webhook is slow.
	var wg sync.WaitGroup
	wg.Add(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		wg.Done()
	}))
	defer srv.Close()

	n := NewDecisionNotifier(NotifierConfig{WebhookURL: srv.URL})
	start := time.Now()
	n.NotifyPending(context.Background(), pendingGate())
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("NotifyPending blocked for %v — should return immediately", elapsed)
	}
	wg.Wait() // ensure goroutine completes so the test server can shut down cleanly
}

// io_ReadAll avoids importing io in tests where we just need a simple reader helper.
func io_ReadAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 512)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

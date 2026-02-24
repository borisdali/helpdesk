// Command secbot is a security responder that monitors the audit stream for
// critical security events and automatically creates incident bundles for
// investigation. It demonstrates the pattern of automated security response
// while maintaining architectural separation (sub-agents remain independent).
//
// Flow:
//
//	Audit Socket → secbot → REST Gateway → incident_agent → Bundle
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"helpdesk/internal/audit"
)

// volumeTracker tracks event counts for high-volume detection.
type volumeTracker struct {
	mu               sync.Mutex
	eventsThisMinute int
	minuteStart      time.Time
	threshold        int
	alerted          bool // Only alert once per minute window
}

// callbackPayload mirrors IncidentBundleResult from the incident agent.
type callbackPayload struct {
	IncidentID string   `json:"incident_id"`
	BundlePath string   `json:"bundle_path"`
	Timestamp  string   `json:"timestamp"`
	Layers     []string `json:"layers"`
	Errors     []string `json:"errors,omitempty"`
}

// a2aResponse mirrors the gateway JSON response shape.
type a2aResponse struct {
	Agent     string `json:"agent"`
	TaskID    string `json:"task_id,omitempty"`
	State     string `json:"state,omitempty"`
	Text      string `json:"text,omitempty"`
	Artifacts []any  `json:"artifacts,omitempty"`
}

func main() {
	socketPath := flag.String("socket", "/tmp/helpdesk-audit.sock", "Path to audit Unix socket")
	gateway := flag.String("gateway", "http://localhost:8080", "Gateway base URL")
	listen := flag.String("listen", ":9091", "Callback listener address")
	infraKey := flag.String("infra-key", "security-incident", "Infrastructure identifier for incident bundles")
	cooldown := flag.Duration("cooldown", 5*time.Minute, "Minimum time between incident creations")
	maxEventsPerMinute := flag.Int("max-events-per-minute", 100, "Alert threshold for high-volume detection")
	dryRun := flag.Bool("dry-run", false, "Log alerts but don't create incidents")
	verbose := flag.Bool("verbose", false, "Log all received events")
	flag.Parse()

	// Initialize volume tracker
	volTracker := &volumeTracker{
		threshold:   *maxEventsPerMinute,
		minuteStart: time.Now(),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	logPhase(1, "Startup")
	logf("Audit socket:  %s", *socketPath)
	logf("Gateway:       %s", *gateway)
	logf("Callback:      %s", *listen)
	logf("Cooldown:      %s", *cooldown)
	logf("Max events/min: %d", *maxEventsPerMinute)
	logf("Dry run:       %v", *dryRun)
	fmt.Println()

	// Start callback server
	callbackCh := make(chan callbackPayload, 10)
	srv := startCallbackServer(*listen, callbackCh)
	defer srv.Shutdown(context.Background())

	logPhase(2, "Connect to Audit Stream")
	fmt.Println()

	logPhase(3, "Monitoring for Security Events")
	logf("Watching for: %s", strings.Join(alertTypeList(), ", "))
	fmt.Println()

	// Event processing runs in a goroutine that reconnects automatically if
	// auditd restarts or drops the connection. Reading and processing are
	// decoupled: a fast reader goroutine keeps the socket buffer drained while
	// the processor handles slow operations (e.g. gatewayPOST for incidents).
	go func() {
		var lastIncidentTime time.Time
		eventCount := 0

		for {
			if ctx.Err() != nil {
				return
			}

			conn, err := net.Dial("unix", *socketPath)
			if err != nil {
				logf("WARN: Cannot connect to audit socket: %v — retrying in 5s", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			logf("Connected to audit stream")

			// eventCh buffers parsed events so the reader goroutine is never
			// blocked by slow downstream work.
			eventCh := make(chan audit.Event, 64)

			// Fast reader — sole owner of conn; closes eventCh when done.
			go func(c net.Conn) {
				defer close(eventCh)
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					line := scanner.Bytes()
					if len(line) == 0 {
						continue
					}
					var event audit.Event
					if err := json.Unmarshal(line, &event); err != nil {
						logf("WARN: Failed to parse event: %v", err)
						continue
					}
					select {
					case eventCh <- event:
					case <-ctx.Done():
						return
					}
				}
				if err := scanner.Err(); err != nil && ctx.Err() == nil {
					logf("WARN: Socket read error: %v — will reconnect", err)
				}
			}(conn)

			// Processor — runs until eventCh is closed (connection lost).
			for event := range eventCh {
				eventCount++
				if *verbose {
					logf("EVENT #%d: %s (type=%s)", eventCount, event.EventID, event.EventType)
				}

				alertType := volTracker.recordAndCheck()
				if alertType == "" {
					alertType = detectSecurityAlert(&event)
				}
				if alertType == "" {
					continue
				}

				logf("SECURITY ALERT: %s", alertType)
				logf("  Event ID:  %s", event.EventID)
				logf("  Trace ID:  %s", event.TraceID)
				logf("  Time:      %s", event.Timestamp.Format(time.RFC3339))
				if event.Tool != nil {
					logf("  Tool:      %s", event.Tool.Name)
					logf("  Agent:     %s", event.Tool.Agent)
				}

				if time.Since(lastIncidentTime) < *cooldown {
					remaining := *cooldown - time.Since(lastIncidentTime)
					logf("  Skipping incident creation (cooldown: %s remaining)", remaining.Round(time.Second))
					fmt.Println()
					continue
				}

				if *dryRun {
					logf("  [DRY RUN] Would create incident bundle")
					fmt.Println()
					continue
				}

				fmt.Println()
				logPhase(4, "Creating Security Incident Bundle")

				callbackHost, callbackPort := callbackAddr(*listen)
				callbackURL := fmt.Sprintf("http://%s:%s/callback", callbackHost, callbackPort)
				description := fmt.Sprintf("Security alert: %s (event: %s, trace: %s)",
					alertType, event.EventID, event.TraceID)

				logf("POST /api/v1/incidents")
				logf("  infra_key:    %s", *infraKey)
				logf("  description:  %s", truncate(description, 60))
				logf("  callback_url: %s", callbackURL)

				incResp, err := gatewayPOST(*gateway, "/api/v1/incidents", map[string]any{
					"infra_key":    *infraKey,
					"description":  description,
					"callback_url": callbackURL,
					"layers":       []string{"os", "storage"},
				})
				if err != nil {
					logf("ERROR: Failed to create incident: %v", err)
					fmt.Println()
					continue
				}

				lastIncidentTime = time.Now()
				logf("Incident creation initiated (%d chars response)", len(incResp.Text))
				fmt.Println()

				logPhase(3, "Monitoring for Security Events")
				fmt.Println()
			}

			if ctx.Err() != nil {
				return
			}
			logf("Audit stream disconnected — reconnecting in 5s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	// Handle callbacks in main goroutine
	for {
		select {
		case cb := <-callbackCh:
			logf("CALLBACK RECEIVED:")
			logf("  Incident ID: %s", cb.IncidentID)
			logf("  Bundle:      %s", cb.BundlePath)
			logf("  Layers:      [%s]", strings.Join(cb.Layers, ", "))
			if len(cb.Errors) > 0 {
				logf("  Errors:      %d", len(cb.Errors))
				for _, e := range cb.Errors {
					logf("    - %s", e)
				}
			}
			fmt.Println()

		case <-ctx.Done():
			logf("Shutting down...")
			return
		}
	}
}

// recordAndCheck records an event and returns "high_volume" if threshold exceeded.
func (v *volumeTracker) recordAndCheck() string {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := time.Now()
	if now.Sub(v.minuteStart) >= time.Minute {
		// Reset for new minute window
		v.eventsThisMinute = 0
		v.minuteStart = now
		v.alerted = false
	}

	v.eventsThisMinute++

	// Only alert once per minute window when first exceeding threshold
	if v.eventsThisMinute == v.threshold+1 && !v.alerted {
		v.alerted = true
		return "high_volume"
	}

	return ""
}

// detectSecurityAlert checks if an event represents a critical security alert.
// Returns the alert type or empty string if not a security alert.
func detectSecurityAlert(event *audit.Event) string {
	// Check for chain integrity issues (detected by auditor)
	if event.EventHash != "" && !audit.VerifyEventHash(event) {
		return "hash_mismatch"
	}

	// Check for unauthorized destructive operations
	if event.ActionClass == audit.ActionDestructive {
		if event.Approval == nil ||
			(event.Approval.Status != audit.ApprovalApproved &&
				event.Approval.Status != audit.ApprovalAutoApproved) {
			return "unauthorized_destructive"
		}
	}

	// Check for tool errors that might indicate attack patterns
	if event.Tool != nil && event.Tool.Error != "" {
		errLower := strings.ToLower(event.Tool.Error)
		// SQL injection attempts often cause syntax errors
		if strings.Contains(errLower, "syntax error") &&
			strings.Contains(errLower, "sql") {
			return "potential_sql_injection"
		}
		// Command injection attempts
		if strings.Contains(errLower, "command not found") ||
			strings.Contains(errLower, "permission denied") {
			return "potential_command_injection"
		}
	}

	// For now, we rely on the auditor to detect most patterns and
	// we watch for the specific event types it generates
	// In a production system, you might duplicate some detection here
	// for redundancy

	return ""
}

func alertTypeList() []string {
	return []string{
		"high_volume",
		"hash_mismatch",
		"unauthorized_destructive",
		"potential_sql_injection",
		"potential_command_injection",
	}
}

// --- HTTP helpers ---

func gatewayPOST(baseURL, path string, payload map[string]any) (*a2aResponse, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(baseURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}

	var result a2aResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("POST %s: decode: %w", path, err)
	}
	return &result, nil
}

// --- Callback server ---

func startCallbackServer(addr string, ch chan<- callbackPayload) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /callback", func(w http.ResponseWriter, r *http.Request) {
		var p callbackPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"received"}`))

		select {
		case ch <- p:
		default:
		}
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go srv.ListenAndServe()
	return srv
}

func callbackAddr(listen string) (host, port string) {
	h, p, err := net.SplitHostPort(listen)
	if err != nil {
		return "localhost", "9091"
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		if ip := outboundIP(); ip != "" {
			return ip, p
		}
		return "localhost", p
	}
	return h, p
}

func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

// --- Output formatting ---

func logf(format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

func logPhase(num int, name string) {
	line := fmt.Sprintf("Phase %d: %s", num, name)
	pad := 50 - len(line)
	if pad < 4 {
		pad = 4
	}
	fmt.Println()
	logf("%s %s %s", strings.Repeat("\u2500", 2), line, strings.Repeat("\u2500", pad))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

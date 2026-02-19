// Command srebot simulates an observability watcher / SRE bot that calls the
// Helpdesk gateway REST API to check database health. When anomalies are
// detected it asks the AI-powered database agent to investigate autonomously,
// then triggers an incident bundle and waits for the async callback.
package main

import (
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
	"time"
)

// a2aResponse mirrors the gateway JSON response shape.
type a2aResponse struct {
	Agent     string `json:"agent"`
	TaskID    string `json:"task_id,omitempty"`
	State     string `json:"state,omitempty"`
	Text      string `json:"text,omitempty"`
	Artifacts []any  `json:"artifacts,omitempty"`
}

// callbackPayload mirrors IncidentBundleResult from the incident agent.
type callbackPayload struct {
	IncidentID string   `json:"incident_id"`
	BundlePath string   `json:"bundle_path"`
	Timestamp  string   `json:"timestamp"`
	Layers     []string `json:"layers"`
	Errors     []string `json:"errors,omitempty"`
}

// anomalyKeywords are substrings that indicate something is wrong in the
// agent's response text. Matching is case-insensitive.
var anomalyKeywords = []string{
	"error", "fail", "refused", "timeout", "too many",
	"denied", "unreachable", "crash", "oom", "killed",
}

func main() {
	gateway := flag.String("gateway", "http://localhost:8080", "Gateway base URL")
	conn := flag.String("conn", "", "PostgreSQL libpq connection string (required, e.g. host=db port=5432 dbname=mydb user=... password=...)")
	listen := flag.String("listen", ":9090", "Callback listener address")
	infraKey := flag.String("infra-key", "srebot-demo", "Infrastructure identifier for incident bundles")
	cbTimeout := flag.Duration("timeout", 120*time.Second, "How long to wait for the callback")
	force := flag.Bool("force", false, "Skip anomaly check — always run all phases")
	symptom := flag.String("symptom", "Users are reporting database connectivity issues.",
		"Symptom description sent to the AI agent for diagnosis")
	flag.Parse()

	if *conn == "" {
		fmt.Fprintln(os.Stderr, "error: -conn is required (PostgreSQL libpq connection string)")
		flag.Usage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// ── Phase 1: Agent Discovery ──────────────────────────────────────
	logPhase(1, "Agent Discovery")
	logf("GET /api/v1/agents")

	body, err := gatewayGET(*gateway, "/api/v1/agents")
	if err != nil {
		logf("FATAL: %v", err)
		os.Exit(1)
	}

	var agents []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &agents); err != nil {
		logf("FATAL: bad agents response: %v", err)
		os.Exit(1)
	}

	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	logf("Found %d agents: %s", len(agents), strings.Join(names, ", "))
	fmt.Println()

	// ── Phase 2: Health Check ─────────────────────────────────────────
	logPhase(2, "Health Check")
	logf("POST /api/v1/db/check_connection")

	resp, err := gatewayPOST(*gateway, "/api/v1/db/check_connection", map[string]any{
		"connection_string": *conn,
	})
	if err != nil {
		logf("FATAL: %v", err)
		os.Exit(1)
	}

	anomaly := hasAnomaly(resp.Text)
	if anomaly {
		// Find the first matching keyword to show context.
		lower := strings.ToLower(resp.Text)
		for _, kw := range anomalyKeywords {
			if idx := strings.Index(lower, kw); idx != -1 {
				start := idx - 20
				if start < 0 {
					start = 0
				}
				end := idx + len(kw) + 40
				if end > len(resp.Text) {
					end = len(resp.Text)
				}
				logf("Anomaly detected: \"...%s...\"", strings.TrimSpace(resp.Text[start:end]))
				break
			}
		}
	} else {
		logf("Health check OK")
		if !*force {
			logf("No anomalies — all clear.")
			os.Exit(0)
		}
		logf("-force flag set, continuing anyway...")
	}
	fmt.Println()

	// ── Phase 3: AI Diagnosis ─────────────────────────────────────────
	logPhase(3, "AI Diagnosis")

	prompt := fmt.Sprintf(
		"%s The connection_string is `%s`. Please investigate and report your findings.",
		*symptom, *conn,
	)
	logf("POST /api/v1/query  agent=database")
	logf("Prompt: %q", truncate(prompt, 120))

	diagResp, err := gatewayPOST(*gateway, "/api/v1/query", map[string]any{
		"agent":   "database",
		"message": prompt,
	})
	if err != nil {
		logf("WARNING: AI diagnosis failed: %v", err)
		logf("Continuing to incident bundle...")
	} else {
		logf("Agent response (%d chars):", len(diagResp.Text))
		printBox(diagResp.Text)
	}
	fmt.Println()

	// ── Phase 4: Create Incident Bundle ───────────────────────────────
	logPhase(4, "Create Incident Bundle")

	callbackHost, callbackPort := callbackAddr(*listen)
	callbackURL := fmt.Sprintf("http://%s:%s/callback", callbackHost, callbackPort)

	// Start callback server before the POST so it's ready to receive.
	callbackCh := make(chan callbackPayload, 1)
	srv := startCallbackServer(*listen, callbackCh)
	defer srv.Shutdown(context.Background())

	logf("POST /api/v1/incidents")
	logf("  infra_key:    %s", *infraKey)
	logf("  callback_url: %s", callbackURL)

	incResp, err := gatewayPOST(*gateway, "/api/v1/incidents", map[string]any{
		"infra_key":         *infraKey,
		"description":       fmt.Sprintf("SRE bot auto-investigation (anomaly=%v)", anomaly),
		"connection_string": *conn,
		"callback_url":      callbackURL,
	})
	if err != nil {
		logf("FATAL: %v", err)
		os.Exit(1)
	}
	logf("Incident agent responded (%d chars)", len(incResp.Text))
	fmt.Println()

	// ── Phase 5: Awaiting Callback ────────────────────────────────────
	logPhase(5, "Awaiting Callback")
	logf("Listening on %s for POST /callback ...", *listen)

	select {
	case cb := <-callbackCh:
		logf("Callback received!")
		logf("  incident_id: %s", cb.IncidentID)
		logf("  bundle_path: %s", cb.BundlePath)
		logf("  layers:      [%s]", strings.Join(cb.Layers, ", "))
		logf("  errors:      %d", len(cb.Errors))
		for _, e := range cb.Errors {
			logf("    - %s", e)
		}
	case <-time.After(*cbTimeout):
		logf("WARNING: Timed out waiting for callback after %s", *cbTimeout)
	case <-ctx.Done():
		logf("Interrupted.")
	}

	logf("Done.")
}

// ── HTTP helpers ──────────────────────────────────────────────────────────

func gatewayGET(baseURL, path string) ([]byte, error) {
	resp, err := http.Get(baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func gatewayPOST(baseURL, path string, payload map[string]any) (*a2aResponse, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(data))
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

// ── Anomaly detection ─────────────────────────────────────────────────────

func hasAnomaly(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range anomalyKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// ── Callback server ───────────────────────────────────────────────────────

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
		return "localhost", "9090"
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

// ── Output formatting ─────────────────────────────────────────────────────

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
	logf("%s %s %s", strings.Repeat("\u2500", 2), line, strings.Repeat("\u2500", pad))
}

// printBox prints text inside a Unicode box, indented to align with log output.
func printBox(text string) {
	const indent = "           " // align with log timestamp prefix
	const maxWidth = 68

	lines := wrapText(text, maxWidth)
	border := strings.Repeat("\u2500", maxWidth+2)

	fmt.Printf("%s\u250c%s\u2510\n", indent, border)
	for _, line := range lines {
		pad := maxWidth - displayWidth(line)
		if pad < 0 {
			pad = 0
		}
		fmt.Printf("%s\u2502 %s%s \u2502\n", indent, line, strings.Repeat(" ", pad))
	}
	fmt.Printf("%s\u2514%s\u2518\n", indent, border)
}

// wrapText splits text into lines no wider than maxWidth, preserving existing
// line breaks.
func wrapText(text string, maxWidth int) []string {
	var result []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			result = append(result, "")
			continue
		}
		for len(paragraph) > 0 {
			if displayWidth(paragraph) <= maxWidth {
				result = append(result, paragraph)
				break
			}
			// Find a break point.
			cut := maxWidth
			if cut > len(paragraph) {
				cut = len(paragraph)
			}
			// Try to break at a space.
			if idx := strings.LastIndex(paragraph[:cut], " "); idx > 0 {
				cut = idx
			}
			result = append(result, paragraph[:cut])
			paragraph = strings.TrimLeft(paragraph[cut:], " ")
		}
	}
	return result
}

// displayWidth returns the visible width of a string (byte length; good enough
// for ASCII-dominant agent output).
func displayWidth(s string) int {
	return len(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// helpdesk-client is an authenticated interactive CLI for the aiHelpDesk gateway.
//
// It replaces direct kubectl-exec / docker-compose-run sessions with an authenticated
// connection through the gateway, so every operator query carries a verified identity
// and purpose in the audit trail.
//
// Usage — interactive REPL:
//
//	helpdesk-client --gateway http://helpdesk:8080 --api-key sk-... --purpose diagnostic
//
// Usage — one-shot:
//
//	helpdesk-client --message "is alloydb-on-vm up?" --agent database
//
// Credentials can also be supplied via environment variables:
//
//	HELPDESK_GATEWAY_URL, HELPDESK_CLIENT_USER, HELPDESK_CLIENT_API_KEY,
//	HELPDESK_SESSION_PURPOSE, HELPDESK_SESSION_PURPOSE_NOTE, HELPDESK_CLIENT_AGENT
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"helpdesk/internal/client"
)

const version = "dev"

func main() {
	var (
		gatewayURL   = flag.String("gateway", envOrDefault("HELPDESK_GATEWAY_URL", "http://localhost:8080"), "Gateway `URL`")
		userID       = flag.String("user", os.Getenv("HELPDESK_CLIENT_USER"), "User ID (X-User header; static provider)")
		apiKey       = flag.String("api-key", os.Getenv("HELPDESK_CLIENT_API_KEY"), "API key (Authorization: Bearer; service accounts)")
		purpose      = flag.String("purpose", envOrDefault("HELPDESK_SESSION_PURPOSE", ""), "Session `purpose`: diagnostic, remediation, compliance, emergency")
		purposeNote  = flag.String("purpose-note", os.Getenv("HELPDESK_SESSION_PURPOSE_NOTE"), "Purpose note (e.g. incident ticket number)")
		agentName    = flag.String("agent", envOrDefault("HELPDESK_CLIENT_AGENT", "database"), "Target `agent`: database, k8s, incident, research")
		message      = flag.String("message", "", "One-shot `message` — runs a single query and exits")
		timeout      = flag.Duration("timeout", 5*time.Minute, "Per-request `timeout`")
		showVersion  = flag.Bool("version", false, "Print version and exit")
		planFleetJob = flag.String("plan-fleet-job", "", "Plan a fleet job from a natural language description")
		targetHints  = flag.String("target-hints", "", "Comma-separated target hints for fleet job planning")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("helpdesk-client", version)
		return
	}

	cfg := client.Config{
		GatewayURL:  *gatewayURL,
		UserID:      *userID,
		APIKey:      *apiKey,
		Purpose:     *purpose,
		PurposeNote: *purposeNote,
		Timeout:     *timeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --plan-fleet-job: does not require a pre-ping (the fleet plan endpoint handles its own auth).
	if *planFleetJob != "" {
		if err := runFleetPlan(ctx, cfg, *planFleetJob, *targetHints); err != nil {
			fmt.Fprintf(os.Stderr, "helpdesk-client: %v\n", err)
			os.Exit(1)
		}
		return
	}

	c := client.New(cfg)

	// Verify gateway connectivity and credentials before entering the REPL.
	if err := c.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "helpdesk-client: %v\n", err)
		os.Exit(1)
	}

	if *message != "" {
		// One-shot mode — no session continuity needed.
		if _, err := runQuery(ctx, c, *agentName, "", *message); err != nil {
			fmt.Fprintf(os.Stderr, "helpdesk-client: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Interactive REPL mode.
	runREPL(ctx, c, *agentName)
}

// runREPL runs an interactive read-query-print loop until EOF or "exit".
// It maintains a contextID across turns so the agent retains conversation history.
func runREPL(ctx context.Context, c *client.Client, agentName string) {
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))

	if isTTY {
		fmt.Printf("aiHelpDesk  (%s)\n", c.GatewayURL())
		fmt.Printf("Agent: %s  |  Type \"exit\" or Ctrl-C to quit.\n\n", agentName)
	}

	var contextID string // grows from "" → agent-assigned UUID after first turn

	scanner := bufio.NewScanner(os.Stdin)
	for {
		if isTTY {
			fmt.Print("> ")
		}
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}

		newContextID, err := runQuery(ctx, c, agentName, contextID, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		} else if newContextID != "" {
			contextID = newContextID
		}

		if isTTY {
			fmt.Println()
		}
	}
}

// runQuery sends a single query to the gateway, displays a spinner while waiting,
// then prints the response followed by the trace ID.
// contextID passes an existing agent session; "" starts a new session.
// Returns the context ID from the response (for the caller to pass on the next turn).
func runQuery(ctx context.Context, c *client.Client, agentName, contextID, message string) (string, error) {
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))

	// Spinner — only shown on interactive terminals.
	spinDone := make(chan struct{})
	if isTTY {
		go func() {
			frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			i := 0
			for {
				select {
				case <-spinDone:
					fmt.Print("\r\033[K") // clear spinner line
					return
				case <-time.After(80 * time.Millisecond):
					fmt.Printf("\r%s  Thinking…", frames[i%len(frames)])
					i++
				}
			}
		}()
	}

	resp, err := c.Query(ctx, client.QueryRequest{
		Agent:     agentName,
		Message:   message,
		ContextID: contextID,
	})

	close(spinDone)
	if err != nil {
		return "", err
	}

	fmt.Println(resp.Text)
	if isTTY && resp.TraceID != "" {
		fmt.Printf("[trace: %s  %s]\n", resp.TraceID, time.Now().Format("2006-01-02 15:04:05"))
	}
	return resp.ContextID, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

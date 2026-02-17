// Package main implements the approvals CLI for managing approval requests.
// This tool allows operators to list, approve, deny, and monitor approval requests
// that require human-in-the-loop authorization.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/logging"
)

func main() {
	// Initialize logging first (strips --log-level from args)
	args := logging.InitLogging(os.Args[1:])

	// Get audit service URL from environment or flag
	auditURL := os.Getenv("HELPDESK_AUDIT_URL")

	// Parse global flags
	fs := flag.NewFlagSet("approvals", flag.ExitOnError)
	fs.StringVar(&auditURL, "url", auditURL, "URL of the audit service (or set HELPDESK_AUDIT_URL)")
	outputJSON := fs.Bool("json", false, "Output in JSON format")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: approvals [options] <command> [arguments]

Commands:
  list [--status=pending|approved|denied]  List approval requests
  pending                                  List pending approvals (shorthand for list --status=pending)
  show <approval_id>                       Show details of an approval
  approve <approval_id> --reason "..."     Approve a request
  deny <approval_id> --reason "..."        Deny a request
  cancel <approval_id>                     Cancel a pending request
  watch                                    Watch for new approval requests (interactive)

Options:
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Environment Variables:
  HELPDESK_AUDIT_URL   URL of the audit service (e.g., http://localhost:1199)

Examples:
  approvals pending                         # List pending approvals
  approvals approve apr_abc123 --reason "Verified by ops team"
  approvals deny apr_abc123 --reason "Request not justified"
  approvals watch                           # Interactive approval mode
`)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if auditURL == "" {
		fmt.Fprintln(os.Stderr, "Error: audit service URL required (use --url or set HELPDESK_AUDIT_URL)")
		os.Exit(1)
	}

	remainingArgs := fs.Args()
	if len(remainingArgs) == 0 {
		fs.Usage()
		os.Exit(1)
	}

	client := audit.NewApprovalClient(auditURL)
	ctx := context.Background()

	command := remainingArgs[0]
	cmdArgs := remainingArgs[1:]

	var err error
	switch command {
	case "list":
		err = cmdList(ctx, client, cmdArgs, *outputJSON, auditURL)
	case "pending":
		err = cmdList(ctx, client, []string{"--status=pending"}, *outputJSON, auditURL)
	case "show":
		err = cmdShow(ctx, client, cmdArgs, *outputJSON)
	case "approve":
		err = cmdApprove(ctx, cmdArgs, auditURL)
	case "deny":
		err = cmdDeny(ctx, cmdArgs, auditURL)
	case "cancel":
		err = cmdCancel(ctx, client, cmdArgs)
	case "watch":
		err = cmdWatch(ctx, client, auditURL)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		fs.Usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdList(ctx context.Context, client *audit.ApprovalClient, args []string, outputJSON bool, auditURL string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	status := fs.String("status", "", "Filter by status (pending, approved, denied, expired)")
	agent := fs.String("agent", "", "Filter by agent name")
	traceID := fs.String("trace-id", "", "Filter by trace ID")
	limit := fs.Int("limit", 20, "Maximum number of results")
	if err := fs.Parse(args); err != nil {
		return err
	}

	approvals, err := client.ListApprovals(ctx, audit.ApprovalListOptions{
		Status:    *status,
		AgentName: *agent,
		TraceID:   *traceID,
		Limit:     *limit,
	})
	if err != nil {
		return fmt.Errorf("list approvals: %w", err)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(approvals)
	}

	if len(approvals) == 0 {
		fmt.Println("No approvals found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tACTION\tTOOL\tAGENT\tREQUESTED\tEXPIRES")
	for _, a := range approvals {
		expiresIn := ""
		if !a.ExpiresAt.IsZero() {
			remaining := time.Until(a.ExpiresAt)
			if remaining > 0 {
				expiresIn = formatDuration(remaining)
			} else {
				expiresIn = "expired"
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ApprovalID,
			statusIcon(a.Status)+" "+a.Status,
			a.ActionClass,
			truncate(a.ToolName, 20),
			a.AgentName,
			a.RequestedAt.Format("15:04:05"),
			expiresIn,
		)
	}
	w.Flush()

	return nil
}

func cmdShow(ctx context.Context, client *audit.ApprovalClient, args []string, outputJSON bool) error {
	if len(args) == 0 {
		return fmt.Errorf("approval ID required")
	}

	approvalID := args[0]
	approval, err := client.GetApproval(ctx, approvalID)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(approval)
	}

	fmt.Printf("Approval ID:    %s\n", approval.ApprovalID)
	fmt.Printf("Status:         %s %s\n", statusIcon(approval.Status), approval.Status)
	fmt.Printf("Action Class:   %s\n", approval.ActionClass)
	if approval.ToolName != "" {
		fmt.Printf("Tool:           %s\n", approval.ToolName)
	}
	if approval.AgentName != "" {
		fmt.Printf("Agent:          %s\n", approval.AgentName)
	}
	if approval.ResourceType != "" {
		fmt.Printf("Resource:       %s/%s\n", approval.ResourceType, approval.ResourceName)
	}
	fmt.Printf("Requested By:   %s\n", approval.RequestedBy)
	fmt.Printf("Requested At:   %s\n", approval.RequestedAt.Format(time.RFC3339))
	if !approval.ExpiresAt.IsZero() {
		remaining := time.Until(approval.ExpiresAt)
		if remaining > 0 {
			fmt.Printf("Expires In:     %s\n", formatDuration(remaining))
		} else {
			fmt.Printf("Expired At:     %s\n", approval.ExpiresAt.Format(time.RFC3339))
		}
	}
	if approval.TraceID != "" {
		fmt.Printf("Trace ID:       %s\n", approval.TraceID)
	}
	if approval.EventID != "" {
		fmt.Printf("Event ID:       %s\n", approval.EventID)
	}
	if approval.PolicyName != "" {
		fmt.Printf("Policy:         %s\n", approval.PolicyName)
	}
	if approval.ResolvedBy != "" {
		fmt.Printf("Resolved By:    %s\n", approval.ResolvedBy)
		fmt.Printf("Resolved At:    %s\n", approval.ResolvedAt.Format(time.RFC3339))
	}
	if approval.ResolutionReason != "" {
		fmt.Printf("Reason:         %s\n", approval.ResolutionReason)
	}
	if len(approval.RequestContext) > 0 {
		fmt.Println("Request Context:")
		for k, v := range approval.RequestContext {
			fmt.Printf("  %s: %v\n", k, v)
		}
	}

	return nil
}

func cmdApprove(ctx context.Context, args []string, auditURL string) error {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	reason := fs.String("reason", "", "Reason for approval")
	validFor := fs.Int("valid-for", 0, "Approval valid for N minutes (0 = no expiration)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) == 0 {
		return fmt.Errorf("approval ID required")
	}

	approvalID := fs.Args()[0]
	approvedBy := os.Getenv("USER")
	if approvedBy == "" {
		approvedBy = "operator"
	}

	// Build request body
	body := map[string]any{
		"approved_by": approvedBy,
	}
	if *reason != "" {
		body["reason"] = *reason
	}
	if *validFor > 0 {
		body["valid_for_minutes"] = *validFor
	}

	jsonBody, _ := json.Marshal(body)
	resp, err := doHTTPRequest(ctx, "POST", auditURL+"/v1/approvals/"+approvalID+"/approve", jsonBody)
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}

	var approval audit.StoredApproval
	if err := json.Unmarshal(resp, &approval); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Printf("Approved: %s\n", approval.ApprovalID)
	fmt.Printf("  Status:      %s\n", approval.Status)
	fmt.Printf("  Approved By: %s\n", approval.ResolvedBy)
	if !approval.ApprovalValidUntil.IsZero() {
		fmt.Printf("  Valid Until: %s\n", approval.ApprovalValidUntil.Format(time.RFC3339))
	}

	return nil
}

func cmdDeny(ctx context.Context, args []string, auditURL string) error {
	fs := flag.NewFlagSet("deny", flag.ExitOnError)
	reason := fs.String("reason", "", "Reason for denial (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) == 0 {
		return fmt.Errorf("approval ID required")
	}
	if *reason == "" {
		return fmt.Errorf("--reason is required when denying")
	}

	approvalID := fs.Args()[0]
	deniedBy := os.Getenv("USER")
	if deniedBy == "" {
		deniedBy = "operator"
	}

	body := map[string]any{
		"denied_by": deniedBy,
		"reason":    *reason,
	}

	jsonBody, _ := json.Marshal(body)
	resp, err := doHTTPRequest(ctx, "POST", auditURL+"/v1/approvals/"+approvalID+"/deny", jsonBody)
	if err != nil {
		return fmt.Errorf("deny: %w", err)
	}

	var approval audit.StoredApproval
	if err := json.Unmarshal(resp, &approval); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Printf("Denied: %s\n", approval.ApprovalID)
	fmt.Printf("  Status:    %s\n", approval.Status)
	fmt.Printf("  Denied By: %s\n", approval.ResolvedBy)
	fmt.Printf("  Reason:    %s\n", approval.ResolutionReason)

	return nil
}

func cmdCancel(ctx context.Context, client *audit.ApprovalClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("approval ID required")
	}

	approvalID := args[0]
	cancelledBy := os.Getenv("USER")
	if cancelledBy == "" {
		cancelledBy = "operator"
	}

	if err := client.CancelApproval(ctx, approvalID, cancelledBy, "Cancelled via CLI"); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}

	fmt.Printf("Cancelled: %s\n", approvalID)
	return nil
}

func cmdWatch(ctx context.Context, client *audit.ApprovalClient, auditURL string) error {
	fmt.Println("Watching for pending approvals... (press Ctrl+C to exit)")
	fmt.Println("Enter approval ID to approve, or !<ID> to deny")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Track seen approvals to avoid duplicate output
	seen := make(map[string]bool)

	// Initial check
	showPending(ctx, client, seen)

	inputCh := make(chan string)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			inputCh <- strings.TrimSpace(line)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			showPending(ctx, client, seen)
		case input := <-inputCh:
			if input == "" {
				continue
			}
			if strings.HasPrefix(input, "!") {
				// Deny
				id := strings.TrimPrefix(input, "!")
				fmt.Print("Reason for denial: ")
				reason, _ := reader.ReadString('\n')
				reason = strings.TrimSpace(reason)
				if reason == "" {
					fmt.Println("Denial requires a reason")
					continue
				}
				if err := denyApproval(ctx, id, reason, auditURL); err != nil {
					fmt.Printf("Error: %v\n", err)
				}
			} else {
				// Approve
				fmt.Print("Reason for approval (optional): ")
				reason, _ := reader.ReadString('\n')
				reason = strings.TrimSpace(reason)
				if err := approveApproval(ctx, input, reason, auditURL); err != nil {
					fmt.Printf("Error: %v\n", err)
				}
			}
		}
	}
}

func showPending(ctx context.Context, client *audit.ApprovalClient, seen map[string]bool) {
	approvals, err := client.ListApprovals(ctx, audit.ApprovalListOptions{
		Status: "pending",
		Limit:  20,
	})
	if err != nil {
		fmt.Printf("Error fetching pending: %v\n", err)
		return
	}

	for _, a := range approvals {
		if seen[a.ApprovalID] {
			continue
		}
		seen[a.ApprovalID] = true

		fmt.Println()
		fmt.Printf("NEW APPROVAL REQUEST: %s\n", a.ApprovalID)
		fmt.Printf("  Action:    %s\n", a.ActionClass)
		fmt.Printf("  Tool:      %s\n", a.ToolName)
		fmt.Printf("  Agent:     %s\n", a.AgentName)
		fmt.Printf("  Requested: %s by %s\n", a.RequestedAt.Format("15:04:05"), a.RequestedBy)
		if !a.ExpiresAt.IsZero() {
			remaining := time.Until(a.ExpiresAt)
			if remaining > 0 {
				fmt.Printf("  Expires:   in %s\n", formatDuration(remaining))
			}
		}
		fmt.Printf("  > Enter '%s' to approve, '!%s' to deny\n", a.ApprovalID, a.ApprovalID)
	}
}

func approveApproval(ctx context.Context, id, reason, auditURL string) error {
	approvedBy := os.Getenv("USER")
	if approvedBy == "" {
		approvedBy = "operator"
	}

	body := map[string]any{
		"approved_by": approvedBy,
	}
	if reason != "" {
		body["reason"] = reason
	}

	jsonBody, _ := json.Marshal(body)
	_, err := doHTTPRequest(ctx, "POST", auditURL+"/v1/approvals/"+id+"/approve", jsonBody)
	if err != nil {
		return err
	}

	fmt.Printf("Approved: %s\n", id)
	return nil
}

func denyApproval(ctx context.Context, id, reason, auditURL string) error {
	deniedBy := os.Getenv("USER")
	if deniedBy == "" {
		deniedBy = "operator"
	}

	body := map[string]any{
		"denied_by": deniedBy,
		"reason":    reason,
	}

	jsonBody, _ := json.Marshal(body)
	_, err := doHTTPRequest(ctx, "POST", auditURL+"/v1/approvals/"+id+"/deny", jsonBody)
	if err != nil {
		return err
	}

	fmt.Printf("Denied: %s\n", id)
	return nil
}

// Helper functions

func statusIcon(status string) string {
	switch status {
	case "pending":
		return "[?]"
	case "approved":
		return "[+]"
	case "denied":
		return "[-]"
	case "expired":
		return "[X]"
	case "cancelled":
		return "[~]"
	default:
		return "[.]"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func doHTTPRequest(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

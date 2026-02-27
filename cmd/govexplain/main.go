// Package main implements govexplain — a CLI for querying the policy explainability API.
//
// It supports three modes:
//
//  1. Hypothetical check — "what would happen if I tried this action?"
//     govexplain --gateway http://localhost:8080 \
//     --resource database:prod-db --action write --tags production
//
//  2. Retrospective — "why was this audit event denied?"
//     govexplain --gateway http://localhost:8080 --event tool_a1b2c3d4
//
//  3. List — show explanations for multiple recent policy decisions
//     govexplain --auditd http://localhost:1199 --list --since 1h
//     govexplain --auditd http://localhost:1199 --list --effect deny
//
// Exit codes:
//
//	0  allowed (or all events allowed in list mode)
//	1  denied (or at least one deny in list mode)
//	2  requires approval (or at least one require_approval, no denials)
//	3  error (network, missing args, etc.)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"helpdesk/internal/policy"
)

func main() {
	gateway := flag.String("gateway", envOrDefault("HELPDESK_GATEWAY_URL", "http://localhost:8080"), "Gateway base URL (requires gateway + auditd)")
	auditd := flag.String("auditd", envOrDefault("HELPDESK_AUDIT_URL", ""), "Auditd base URL — bypasses the gateway (e.g. http://localhost:1199)")
	policyFile := flag.String("policy-file", envOrDefault("HELPDESK_POLICY_FILE", ""), "Policy file for local evaluation — no server required (e.g. policies.yaml)")
	event := flag.String("event", "", "Audit event ID to explain (retrospective mode)")
	resource := flag.String("resource", "", "Resource to check: type:name (e.g. database:prod-db)")
	action := flag.String("action", "", "Action to check: read, write, destructive")
	tags := flag.String("tags", "", "Comma-separated resource tags (e.g. production,critical)")
	userID := flag.String("user", "", "Evaluate as a specific user ID")
	role := flag.String("role", "", "Evaluate with a specific role")
	asJSON := flag.Bool("json", false, "Output raw JSON instead of human-readable text")

	// List mode flags
	list := flag.Bool("list", false, "List policy decisions (batch retrospective mode)")
	since := flag.String("since", "", "Show events newer than this: duration (1h, 30m) or RFC3339 timestamp")
	session := flag.String("session", "", "Filter by session ID")
	trace := flag.String("trace", "", "Filter by exact trace ID")
	tracePrefix := flag.String("trace-prefix", "", "Filter by trace ID prefix (e.g. chk_, sess_, dbagent_)")
	effect := flag.String("effect", "", "Filter by effect: allow, deny, require_approval")
	limit := flag.Int("limit", 20, "Maximum number of events to show in list mode")
	table := flag.Bool("table", false, "Compact tabular output: one row per event")

	flag.Parse()

	// Local mode: when --policy-file (or HELPDESK_POLICY_FILE) is set and the
	// request is a hypothetical check (--resource + --action), evaluate the
	// policy directly without talking to any server. This lets operators test
	// policy files from the binary tarball before deploying them.
	if *policyFile != "" && *resource != "" && *action != "" && *event == "" && !*list {
		parts := strings.SplitN(*resource, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "error: --resource must be TYPE:NAME (e.g. database:prod-db)")
			os.Exit(3)
		}
		os.Exit(runLocalExplain(*policyFile, parts[0], parts[1], *action, *tags, *userID, *role, *asJSON))
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// --auditd bypasses the gateway and talks directly to auditd.
	// auditd exposes /v1/governance/explain and /v1/events/{id} natively.
	if *auditd != "" {
		base := strings.TrimRight(*auditd, "/")
		if *list {
			os.Exit(runList(client, base+"/v1/events", *since, *session, *trace, *tracePrefix, *effect, *limit, *asJSON, *table))
		}
		if *event != "" {
			os.Exit(runRetrospectiveDirect(client, *auditd, *event, *asJSON))
		}
		if *resource == "" || *action == "" {
			printUsage()
			os.Exit(3)
		}
		parts := strings.SplitN(*resource, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "error: --resource must be TYPE:NAME (e.g. database:prod-db)")
			os.Exit(3)
		}
		os.Exit(runHypotheticalDirect(client, *auditd, parts[0], parts[1], *action, *tags, *userID, *role, *asJSON))
	}

	if *list {
		base := strings.TrimRight(*gateway, "/")
		os.Exit(runList(client, base+"/api/v1/governance/events", *since, *session, *trace, *tracePrefix, *effect, *limit, *asJSON, *table))
	}

	if *event != "" {
		os.Exit(runRetrospective(client, *gateway, *event, *asJSON))
	}

	if *resource == "" || *action == "" {
		printUsage()
		os.Exit(3)
	}

	parts := strings.SplitN(*resource, ":", 2)
	if len(parts) != 2 {
		fmt.Fprintln(os.Stderr, "error: --resource must be TYPE:NAME (e.g. database:prod-db)")
		os.Exit(3)
	}

	os.Exit(runHypothetical(client, *gateway, parts[0], parts[1], *action, *tags, *userID, *role, *asJSON))
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  Hypothetical (via gateway):  govexplain --resource TYPE:NAME --action ACTION [--tags TAG,…]")
	fmt.Fprintln(os.Stderr, "  Hypothetical (direct):       govexplain --auditd http://localhost:1199 --resource TYPE:NAME --action ACTION")
	fmt.Fprintln(os.Stderr, "  Retrospective (via gateway): govexplain --event EVENT_ID")
	fmt.Fprintln(os.Stderr, "  Retrospective (direct):      govexplain --auditd http://localhost:1199 --event EVENT_ID")
	fmt.Fprintln(os.Stderr, "  List (direct):               govexplain --auditd http://localhost:1199 --list [--since 1h] [--effect deny]")
	fmt.Fprintln(os.Stderr, "  List (via gateway):          govexplain --list [--since 1h] [--session ID] [--limit 50]")
	fmt.Fprintln(os.Stderr, "  List by trace prefix:        govexplain --auditd http://localhost:1199 --list --trace-prefix chk_")
}

// parsedEvent holds a decoded audit event alongside its extracted effect string.
type parsedEvent struct {
	raw    map[string]json.RawMessage
	effStr string
}

// runList fetches multiple policy_decision events and prints their explanations.
func runList(client *http.Client, baseURL, since, session, trace, tracePrefix, effectFilter string, limit int, asJSON, asTable bool) int {
	q := url.Values{}
	q.Set("event_type", "policy_decision")
	if session != "" {
		q.Set("session_id", session)
	}
	if trace != "" {
		q.Set("trace_id", trace)
	}
	if tracePrefix != "" {
		q.Set("trace_id_prefix", tracePrefix)
	}
	if since != "" {
		t, err := parseSince(since)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: --since:", err)
			return 3
		}
		q.Set("since", t.UTC().Format(time.RFC3339))
	}

	endpoint := baseURL + "?" + q.Encode()
	resp, err := client.Get(endpoint)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 3
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading response:", err)
		return 3
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "error: HTTP %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return 3
	}

	if asJSON {
		fmt.Println(string(body))
		return 0
	}

	var events []json.RawMessage
	if err := json.Unmarshal(body, &events); err != nil {
		fmt.Fprintln(os.Stderr, "error parsing response:", err)
		return 3
	}

	// Apply client-side effect filter and limit up front.
	var filtered []parsedEvent
	for _, raw := range events {
		if len(filtered) >= limit {
			break
		}
		var ev map[string]json.RawMessage
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		effStr := extractEffect(ev)
		if effectFilter != "" && effStr != effectFilter {
			continue
		}
		filtered = append(filtered, parsedEvent{ev, effStr})
	}

	if len(filtered) == 0 {
		fmt.Println("No policy decision events found.")
		return 0
	}

	// Compute worst exit code across all events.
	result := 0
	for _, e := range filtered {
		code := effectToCode(e.effStr)
		if code == 1 || (result != 1 && code == 2) {
			result = code
		}
	}

	if asTable {
		return printTable(filtered)
	}

	sep := strings.Repeat("─", 60)
	for i, e := range filtered {
		if i > 0 {
			fmt.Println(sep)
		}

		var eventID, ts string
		if v, ok := e.raw["event_id"]; ok {
			json.Unmarshal(v, &eventID)
		}
		if v, ok := e.raw["timestamp"]; ok {
			json.Unmarshal(v, &ts)
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				ts = t.Local().Format("2006-01-02 15:04:05")
			}
		}
		fmt.Printf("%s  %s\n", eventID, ts)

		if pdRaw, ok := e.raw["policy_decision"]; ok {
			var pd map[string]json.RawMessage
			if json.Unmarshal(pdRaw, &pd) == nil {
				if expl, ok := pd["explanation"]; ok {
					var s string
					if json.Unmarshal(expl, &s) == nil && s != "" {
						fmt.Println(s)
					}
				}
			}
		}
	}

	return result
}

// printTable renders policy decision events as a compact tabular summary.
// Columns: EVENT  TIME  EFFECT  ACTION  RESOURCE  POLICY  TRACE
func printTable(events []parsedEvent) int {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "EVENT\tTIME\tEFFECT\tACTION\tRESOURCE\tPOLICY\tTRACE")

	for _, e := range events {
		var eventID, ts, traceID string
		var resourceType, resourceName, action, policyName string

		if v, ok := e.raw["event_id"]; ok {
			json.Unmarshal(v, &eventID)
		}
		if v, ok := e.raw["timestamp"]; ok {
			json.Unmarshal(v, &ts)
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				ts = t.Local().Format("01-02 15:04:05")
			}
		}
		if v, ok := e.raw["trace_id"]; ok {
			json.Unmarshal(v, &traceID)
		}
		if pdRaw, ok := e.raw["policy_decision"]; ok {
			var pd map[string]json.RawMessage
			if json.Unmarshal(pdRaw, &pd) == nil {
				if v, ok := pd["resource_type"]; ok {
					json.Unmarshal(v, &resourceType)
				}
				if v, ok := pd["resource_name"]; ok {
					json.Unmarshal(v, &resourceName)
				}
				if v, ok := pd["action"]; ok {
					json.Unmarshal(v, &action)
				}
				if v, ok := pd["policy_name"]; ok {
					json.Unmarshal(v, &policyName)
				}
			}
		}

		resource := resourceType + ":" + resourceName
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			eventID, ts, strings.ToUpper(e.effStr), action, resource, policyName, traceID)
	}

	w.Flush()
	return 0
}

// parseSince parses --since as a duration or RFC3339 timestamp.
// Supported duration formats: Go standard (1h, 30m, 45s), plus d (days) and w (weeks).
func parseSince(s string) (time.Time, error) {
	// d / w shorthand not in Go's time.ParseDuration.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if strings.HasSuffix(s, "w") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err == nil && n > 0 {
			return time.Now().Add(-time.Duration(n) * 7 * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected duration (e.g. 1h, 30m, 7d, 2w) or RFC3339 timestamp, got %q", s)
}

func effectToCode(effect string) int {
	switch effect {
	case "allow":
		return 0
	case "deny":
		return 1
	case "require_approval":
		return 2
	default:
		return 3
	}
}

// runHypotheticalDirect talks to auditd's native /v1/governance/explain endpoint.
// Only auditd needs to be running — no gateway required.
func runHypotheticalDirect(client *http.Client, auditdURL, resourceType, resourceName, action, tags, userID, role string, asJSON bool) int {
	q := url.Values{}
	q.Set("resource_type", resourceType)
	q.Set("resource_name", resourceName)
	q.Set("action", action)
	if tags != "" {
		q.Set("tags", tags)
	}
	if userID != "" {
		q.Set("user_id", userID)
	}
	if role != "" {
		q.Set("role", role)
	}
	endpoint := strings.TrimRight(auditdURL, "/") + "/v1/governance/explain?" + q.Encode()
	return doExplainRequest(client, endpoint, asJSON)
}

// runRetrospectiveDirect talks to auditd's native /v1/events/{id} endpoint.
// Only auditd needs to be running — no gateway required.
func runRetrospectiveDirect(client *http.Client, auditdURL, eventID string, asJSON bool) int {
	endpoint := strings.TrimRight(auditdURL, "/") + "/v1/events/" + url.PathEscape(eventID)
	return doExplainRequest(client, endpoint, asJSON)
}

func runHypothetical(client *http.Client, gateway, resourceType, resourceName, action, tags, userID, role string, asJSON bool) int {
	q := url.Values{}
	q.Set("resource_type", resourceType)
	q.Set("resource_name", resourceName)
	q.Set("action", action)
	if tags != "" {
		q.Set("tags", tags)
	}
	if userID != "" {
		q.Set("user_id", userID)
	}
	if role != "" {
		q.Set("role", role)
	}

	endpoint := strings.TrimRight(gateway, "/") + "/api/v1/governance/explain?" + q.Encode()
	return doExplainRequest(client, endpoint, asJSON)
}

func runRetrospective(client *http.Client, gateway, eventID string, asJSON bool) int {
	endpoint := strings.TrimRight(gateway, "/") + "/api/v1/governance/events/" + url.PathEscape(eventID)
	return doExplainRequest(client, endpoint, asJSON)
}

func doExplainRequest(client *http.Client, endpoint string, asJSON bool) int {
	resp, err := client.Get(endpoint)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 3
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading response:", err)
		return 3
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "error: HTTP %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return 3
	}

	if asJSON {
		fmt.Println(string(body))
		return exitCodeFromJSON(body)
	}

	// Pretty-print the explanation field if present; fall back to indented JSON.
	var result map[string]json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Println(string(body))
		return 3
	}

	// For a retrospective event, dig into policy_decision.explanation.
	if pd, ok := result["policy_decision"]; ok {
		var pdMap map[string]json.RawMessage
		if json.Unmarshal(pd, &pdMap) == nil {
			if expl, ok := pdMap["explanation"]; ok {
				var s string
				if json.Unmarshal(expl, &s) == nil && s != "" {
					fmt.Println(s)
					return exitCodeFromJSON(pd)
				}
			}
		}
	}

	// For a hypothetical trace, the explanation is at the top level.
	if expl, ok := result["explanation"]; ok {
		var s string
		if json.Unmarshal(expl, &s) == nil && s != "" {
			fmt.Println(s)
			return exitCodeFromJSON(body)
		}
	}

	// Fallback: indented JSON.
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))
	return exitCodeFromJSON(body)
}

// exitCodeFromJSON reads the effect/decision.effect from the JSON and maps it to an exit code.
func exitCodeFromJSON(data json.RawMessage) int {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return 3
	}

	return effectToCode(extractEffect(wrapper))
}

func extractEffect(m map[string]json.RawMessage) string {
	// Hypothetical: {"decision":{"effect":"..."}, ...}
	if decisionRaw, ok := m["decision"]; ok {
		var d map[string]json.RawMessage
		if json.Unmarshal(decisionRaw, &d) == nil {
			if effRaw, ok := d["effect"]; ok {
				var s string
				json.Unmarshal(effRaw, &s)
				return s
			}
		}
	}
	// Retrospective event: {"policy_decision":{"effect":"..."}, ...}
	if pdRaw, ok := m["policy_decision"]; ok {
		var pd map[string]json.RawMessage
		if json.Unmarshal(pdRaw, &pd) == nil {
			if effRaw, ok := pd["effect"]; ok {
				var s string
				json.Unmarshal(effRaw, &s)
				return s
			}
		}
	}
	return ""
}

// runLocalExplain evaluates a hypothetical policy check entirely in-process —
// no gateway or auditd required. Used when --policy-file (or HELPDESK_POLICY_FILE)
// is set.
func runLocalExplain(policyFile, resourceType, resourceName, action, tagsStr, userID, role string, asJSON bool) int {
	cfg, err := policy.LoadFile(policyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error loading policy file:", err)
		return 3
	}

	engine := policy.NewEngine(policy.EngineConfig{PolicyConfig: cfg})

	var tags []string
	for _, t := range strings.Split(tagsStr, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}

	req := policy.Request{
		Principal: policy.RequestPrincipal{
			UserID: userID,
		},
		Resource: policy.RequestResource{
			Type: resourceType,
			Name: resourceName,
			Tags: tags,
		},
		Action: policy.ActionClass(action),
	}
	if role != "" {
		req.Principal.Roles = []string{role}
	}

	trace := engine.Explain(req)

	if asJSON {
		out, _ := json.MarshalIndent(trace, "", "  ")
		fmt.Println(string(out))
		return effectToCode(string(trace.Decision.Effect))
	}

	if trace.Explanation != "" {
		fmt.Println(trace.Explanation)
	} else {
		out, _ := json.MarshalIndent(trace, "", "  ")
		fmt.Println(string(out))
	}
	return effectToCode(string(trace.Decision.Effect))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

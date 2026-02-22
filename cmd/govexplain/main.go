// Package main implements govexplain — a CLI for querying the policy explainability API.
//
// It supports two modes:
//
//  1. Hypothetical check — "what would happen if I tried this action?"
//     govexplain --gateway http://localhost:8080 \
//     --resource database:prod-db --action write --tags production
//
//  2. Retrospective — "why was this audit event denied?"
//     govexplain --gateway http://localhost:8080 --event tool_a1b2c3d4
//
// Exit codes:
//
//	0  allowed
//	1  denied
//	2  requires approval
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
	"strings"
	"time"
)

func main() {
	gateway := flag.String("gateway", envOrDefault("HELPDESK_GATEWAY_URL", "http://localhost:8080"), "Gateway base URL")
	event := flag.String("event", "", "Audit event ID to explain (retrospective mode)")
	resource := flag.String("resource", "", "Resource to check: type:name (e.g. database:prod-db)")
	action := flag.String("action", "", "Action to check: read, write, destructive")
	tags := flag.String("tags", "", "Comma-separated resource tags (e.g. production,critical)")
	userID := flag.String("user", "", "Evaluate as a specific user ID")
	role := flag.String("role", "", "Evaluate with a specific role")
	asJSON := flag.Bool("json", false, "Output raw JSON instead of human-readable text")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}

	if *event != "" {
		os.Exit(runRetrospective(client, *gateway, *event, *asJSON))
	}

	if *resource == "" || *action == "" {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  Hypothetical:   govexplain --resource TYPE:NAME --action ACTION [--tags TAG,…]")
		fmt.Fprintln(os.Stderr, "  Retrospective:  govexplain --event EVENT_ID")
		os.Exit(3)
	}

	parts := strings.SplitN(*resource, ":", 2)
	if len(parts) != 2 {
		fmt.Fprintln(os.Stderr, "error: --resource must be TYPE:NAME (e.g. database:prod-db)")
		os.Exit(3)
	}

	os.Exit(runHypothetical(client, *gateway, parts[0], parts[1], *action, *tags, *userID, *role, *asJSON))
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

	effect := extractEffect(wrapper)
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

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

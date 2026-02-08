#!/usr/bin/env bash
#
# gateway-repl.sh - Interactive REPL-like interface for the Gateway API
#
# This script provides a simple interactive loop that sends queries to the
# Gateway REST API, working around the ADK REPL bug in containers.
#
# Usage:
#   ./scripts/gateway-repl.sh [gateway-url]
#
# Examples:
#   ./scripts/gateway-repl.sh                          # Uses localhost:8080
#   ./scripts/gateway-repl.sh http://gateway:8080      # Custom URL
#
# Prerequisites:
#   - curl and jq installed
#   - Gateway accessible (port-forward if needed):
#     kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080

set -e

GATEWAY_URL="${1:-http://localhost:8080}"

# Colors
BLUE='\033[0;34m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BOLD='\033[1m'
NC='\033[0m'

# Check dependencies
for cmd in curl jq; do
    if ! command -v $cmd &>/dev/null; then
        echo -e "${RED}Error: $cmd is required but not installed.${NC}"
        exit 1
    fi
done

# Test gateway connectivity
echo -e "${CYAN}Connecting to Gateway at ${GATEWAY_URL}...${NC}"
if ! curl -s --connect-timeout 5 "${GATEWAY_URL}/api/v1/agents" >/dev/null 2>&1; then
    echo -e "${RED}Cannot connect to Gateway at ${GATEWAY_URL}${NC}"
    echo ""
    echo "If running in K8s, start port-forward first:"
    echo "  kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080"
    exit 1
fi

# Get available agents (API returns array directly, not {agents: []})
AGENTS=$(curl -s "${GATEWAY_URL}/api/v1/agents" | jq -r '.[].name' 2>/dev/null | tr '\n' ', ' | sed 's/,$//')

echo -e "${GREEN}Connected!${NC} Available agents: ${AGENTS}"
echo ""
echo -e "${BOLD}aiHelpDesk Gateway REPL${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "Type natural language queries (auto-routes to db/k8s/incident agent based on keywords)"
echo -e "Commands: ${YELLOW}/databases${NC}, ${YELLOW}/agents${NC}, ${YELLOW}/tools${NC}, ${YELLOW}/db <tool>${NC}, ${YELLOW}/k8s <tool>${NC}, ${YELLOW}/help${NC}, ${YELLOW}/quit${NC}"
echo ""

show_help() {
    echo ""
    echo -e "${BOLD}Natural Language Queries:${NC}"
    echo ""
    echo -e "  Just type your question. The script auto-routes to the appropriate agent:"
    echo -e "  - Keywords like 'pod', 'k8s', 'node', 'deploy' -> K8s agent"
    echo -e "  - Keywords like 'incident', 'bundle' -> Incident agent"
    echo -e "  - Everything else -> Database agent"
    echo ""
    echo -e "${BOLD}Direct Commands:${NC}"
    echo ""
    echo -e "  ${YELLOW}/databases${NC}           List managed databases from infrastructure.json"
    echo -e "  ${YELLOW}/infra${NC}               Show infrastructure summary"
    echo -e "  ${YELLOW}/agents${NC}              List available agents and their skills"
    echo -e "  ${YELLOW}/tools${NC}               List available tools for /db and /k8s"
    echo -e "  ${YELLOW}/db <tool> <json>${NC}   Call database agent tool directly"
    echo -e "  ${YELLOW}/k8s <tool> <json>${NC}  Call K8s agent tool directly"
    echo -e "  ${YELLOW}/help${NC}               Show this help"
    echo -e "  ${YELLOW}/quit${NC}               Exit the REPL"
    echo ""
    echo -e "${BOLD}Examples:${NC}"
    echo ""
    echo -e "  /databases"
    echo -e "  /db get_server_info {\"connection_string\": \"host=localhost port=5432 dbname=mydb\"}"
    echo -e "  /k8s get_pods {\"namespace\": \"db\"}"
    echo ""
}

list_agents() {
    echo ""
    curl -s "${GATEWAY_URL}/api/v1/agents" | jq -r '
        .[] |
        "Agent: \(.name)\n  Description: \(.description // "N/A")\n  Skills: \((.skills // []) | map(.name) | join(", "))\n"
    '
}

list_tools() {
    echo ""
    echo -e "${BOLD}Database Agent Tools (/db <tool>):${NC}"
    echo ""
    curl -s "${GATEWAY_URL}/api/v1/agents" | jq -r '
        .[] | select(.name == "postgres_database_agent") | .skills // [] | .[] |
        "  \(.name)\n    \(.description // "No description")\n"
    ' 2>/dev/null || echo "  (no tools found)"

    echo -e "${BOLD}K8s Agent Tools (/k8s <tool>):${NC}"
    echo ""
    curl -s "${GATEWAY_URL}/api/v1/agents" | jq -r '
        .[] | select(.name == "k8s_agent") | .skills // [] | .[] |
        "  \(.name)\n    \(.description // "No description")\n"
    ' 2>/dev/null || echo "  (no tools found)"
}

list_databases() {
    echo ""
    local response
    response=$(curl -s "${GATEWAY_URL}/api/v1/databases")

    # Check if endpoint exists (404 returns HTML or error)
    if ! echo "$response" | jq -e . >/dev/null 2>&1; then
        echo -e "${RED}Error: /api/v1/databases endpoint not available.${NC}"
        echo "Rebuild and redeploy the gateway with the latest code."
        return
    fi

    # Check for error response
    local error=$(echo "$response" | jq -r '.error // empty')
    if [[ -n "$error" ]]; then
        echo -e "${RED}Error: $error${NC}"
        return
    fi

    local count=$(echo "$response" | jq -r '.count // 0')
    if [[ "$count" == "0" ]] || [[ "$count" == "null" ]]; then
        echo -e "${YELLOW}No databases configured.${NC}"
        echo "Set HELPDESK_INFRA_CONFIG in the gateway to load infrastructure.json"
        return
    fi

    echo -e "${BOLD}Managed Databases ($count):${NC}"
    echo ""
    echo "$response" | jq -r '
        .databases[] |
        "  \(.id) - \(.name)\n    Connection: \(.connection_string)\n    Hosting: \(.hosting)\(if .k8s_context != "" and .k8s_context != null then "\n    K8s context: \(.k8s_context)" else "" end)\(if .k8s_namespace != "" and .k8s_namespace != null then ", namespace: \(.k8s_namespace)" else "" end)\(if .vm_host != "" and .vm_host != null then "\n    VM host: \(.vm_host)" else "" end)\n"
    ' 2>/dev/null || echo -e "${RED}Failed to parse database list${NC}"
}

list_infrastructure() {
    echo ""
    local response
    response=$(curl -s "${GATEWAY_URL}/api/v1/infrastructure")

    # Check if endpoint exists
    if ! echo "$response" | jq -e . >/dev/null 2>&1; then
        echo -e "${RED}Error: /api/v1/infrastructure endpoint not available.${NC}"
        echo "Rebuild and redeploy the gateway with the latest code."
        return
    fi

    local configured=$(echo "$response" | jq -r '.configured // false')
    if [[ "$configured" != "true" ]]; then
        echo -e "${YELLOW}No infrastructure configured.${NC}"
        echo "Set HELPDESK_INFRA_CONFIG in the gateway to load infrastructure.json"
        return
    fi

    echo "$response" | jq -r '.summary'
}

call_tool() {
    local agent_type="$1"
    local tool="$2"
    local params="$3"

    if [[ -z "$tool" ]]; then
        echo -e "${RED}Usage: /$agent_type <tool_name> [json_params]${NC}"
        return
    fi

    [[ -z "$params" ]] && params="{}"

    local response
    response=$(curl -s -X POST "${GATEWAY_URL}/api/v1/${agent_type}/${tool}" \
        -H "Content-Type: application/json" \
        -d "$params")

    local state=$(echo "$response" | jq -r '.state // "unknown"')
    local text=$(echo "$response" | jq -r '.text // .error // "No response"')

    if [[ "$state" == "completed" ]]; then
        echo -e "${GREEN}[completed]${NC}"
    else
        echo -e "${RED}[${state}]${NC}"
    fi
    echo ""
    echo "$text"
}

query() {
    local q="$1"

    # The /query endpoint requires an agent to be specified
    # Determine which agent based on keywords, default to database
    local agent="database"
    local lower_q=$(echo "$q" | tr '[:upper:]' '[:lower:]')
    if [[ "$lower_q" == *"pod"* ]] || [[ "$lower_q" == *"k8s"* ]] || [[ "$lower_q" == *"kubernetes"* ]] || [[ "$lower_q" == *"node"* ]] || [[ "$lower_q" == *"deploy"* ]]; then
        agent="k8s"
    elif [[ "$lower_q" == *"incident"* ]] || [[ "$lower_q" == *"bundle"* ]]; then
        agent="incident"
    fi

    # Enrich query with infrastructure context if a known database is mentioned
    local enriched_q="$q"
    local db_info=$(curl -s "${GATEWAY_URL}/api/v1/databases" 2>/dev/null)
    if echo "$db_info" | jq -e '.databases' >/dev/null 2>&1; then
        # Check if any database ID or name is mentioned in the query
        while IFS= read -r db_line; do
            local db_id=$(echo "$db_line" | cut -d'|' -f1)
            local db_name=$(echo "$db_line" | cut -d'|' -f2)
            local db_conn=$(echo "$db_line" | cut -d'|' -f3)
            local db_k8s_ns=$(echo "$db_line" | cut -d'|' -f4)
            local db_k8s_ctx=$(echo "$db_line" | cut -d'|' -f5)

            if [[ "$lower_q" == *"$(echo "$db_id" | tr '[:upper:]' '[:lower:]')"* ]] || \
               [[ "$lower_q" == *"$(echo "$db_name" | tr '[:upper:]' '[:lower:]')"* ]]; then
                # Found a matching database - enrich the query
                enriched_q="$q

Context: The '$db_id' database ($db_name) has connection_string: $db_conn"
                if [[ -n "$db_k8s_ns" ]]; then
                    enriched_q="$enriched_q (K8s namespace: $db_k8s_ns)"
                    # Also route K8s-related questions about this DB to k8s agent
                    if [[ "$lower_q" == *"up"* ]] || [[ "$lower_q" == *"running"* ]] || [[ "$lower_q" == *"healthy"* ]] || [[ "$lower_q" == *"status"* ]]; then
                        agent="k8s"
                        enriched_q="Check if pods are healthy in namespace '$db_k8s_ns'. $enriched_q"
                    fi
                fi
                break
            fi
        done < <(echo "$db_info" | jq -r '.databases[] | "\(.id)|\(.name)|\(.connection_string)|\(.k8s_namespace // "")|\(.k8s_context // "")"')
    fi

    local response
    response=$(curl -s -X POST "${GATEWAY_URL}/api/v1/query" \
        -H "Content-Type: application/json" \
        -d "{\"agent\": \"$agent\", \"message\": $(echo "$enriched_q" | jq -Rs .)}")

    local state=$(echo "$response" | jq -r '.state // "unknown"')
    local text=$(echo "$response" | jq -r '.text // .error // "No response"')

    echo ""
    if [[ "$state" == "completed" ]]; then
        echo -e "${GREEN}Agent ($agent):${NC}"
    else
        echo -e "${RED}Error (${state}):${NC}"
    fi
    echo ""
    echo "$text"
}

# Main loop
while true; do
    echo ""
    echo -en "${BLUE}User -> ${NC}"
    read -r input

    # Handle empty input
    [[ -z "$input" ]] && continue

    # Handle commands
    case "$input" in
        /quit|/exit|/q)
            echo -e "${CYAN}Goodbye!${NC}"
            exit 0
            ;;
        /help|/h|\?)
            show_help
            ;;
        /agents)
            list_agents
            ;;
        /tools)
            list_tools
            ;;
        /databases|/dbs)
            list_databases
            ;;
        /infra|/infrastructure)
            list_infrastructure
            ;;
        /db\ *)
            tool=$(echo "$input" | awk '{print $2}')
            params=$(echo "$input" | cut -d' ' -f3-)
            call_tool "db" "$tool" "$params"
            ;;
        /k8s\ *)
            tool=$(echo "$input" | awk '{print $2}')
            params=$(echo "$input" | cut -d' ' -f3-)
            call_tool "k8s" "$tool" "$params"
            ;;
        /*)
            echo -e "${RED}Unknown command: $input${NC}"
            echo "Type /help for available commands"
            ;;
        *)
            query "$input"
            ;;
    esac
done

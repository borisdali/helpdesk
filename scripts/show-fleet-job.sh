#!/usr/bin/env bash
# show-fleet-job.sh — pretty-print fleet job results from any gateway URL.
#
# Usage:
#   show-fleet-job.sh <job_id> [gateway_url] [user]
#
# Examples:
#   show-fleet-job.sh flj_602aaa84
#   show-fleet-job.sh flj_602aaa84 http://localhost:8080 alice@example.com
#
# Defaults:
#   gateway_url  http://localhost:8080
#   user         alice@example.com

set -euo pipefail

JOB_ID="${1:-}"
GATEWAY="${2:-http://localhost:8080}"
USER="${3:-alice@example.com}"

if [[ -z "$JOB_ID" ]]; then
  echo "Usage: $0 <job_id> [gateway_url] [user]" >&2
  exit 1
fi

AUTH_HEADER="X-User: $USER"

# Fetch job status
JOB=$(curl -sf "$GATEWAY/api/v1/fleet/jobs/$JOB_ID" -H "$AUTH_HEADER" 2>/dev/null) || {
  echo "Error: could not fetch job $JOB_ID from $GATEWAY" >&2
  exit 1
}

JOB_STATUS=$(echo "$JOB" | jq -r '.status // "unknown"')
JOB_DESC=$(echo "$JOB"   | jq -r '.description // ""')

echo ""
echo "════════════════════════════════════════════════════════════════════════════"
echo "  Fleet job: $JOB_ID  [$JOB_STATUS]"
[[ -n "$JOB_DESC" ]] && echo "  $JOB_DESC"
echo "════════════════════════════════════════════════════════════════════════════"

# Fetch server list
SERVERS=$(curl -sf "$GATEWAY/api/v1/fleet/jobs/$JOB_ID/servers" -H "$AUTH_HEADER" 2>/dev/null \
  | jq -r '.[].server_name' 2>/dev/null) || SERVERS=""

if [[ -z "$SERVERS" ]]; then
  echo "  (no servers recorded yet)"
  echo ""
  exit 0
fi

# Collect all steps into [{server, steps:[...]}, ...]
ALL_RESULTS="[]"
while IFS= read -r server; do
  STEPS=$(curl -sf \
    "$GATEWAY/api/v1/fleet/jobs/$JOB_ID/servers/$server/steps" \
    -H "$AUTH_HEADER" 2>/dev/null) || STEPS="[]"
  ALL_RESULTS=$(echo "$ALL_RESULTS" \
    | jq --arg s "$server" --argjson st "$STEPS" '. + [{server:$s, steps:$st}]')
done <<<"$SERVERS"

# If any step is get_status_summary, render as a status table.
HAS_SUMMARY=$(echo "$ALL_RESULTS" \
  | jq '[.[].steps[] | select(.tool=="get_status_summary")] | length > 0')

if [[ "$HAS_SUMMARY" == "true" ]]; then
  echo ""
  echo "$ALL_RESULTS" | jq -r '
    ["SERVER", "STAGE", "STATUS", "VERSION", "UPTIME", "CONN", "CACHE HIT%"],
    ["──────", "─────", "──────", "───────", "──────", "────", "──────────"],
    (.[] |
      .server as $srv |
      .steps[] |
      select(.tool == "get_status_summary") |
      .output as $out |
      ($out | try fromjson catch {status:"error",version:"-",uptime:"-",connections:0,max_connections:0,cache_hit_ratio:0}) as $s |
      [$srv,
       (.stage // "-"),
       $s.status,
       ($s.version // "-"),
       ($s.uptime  // "-"),
       (($s.connections // 0 | tostring) + "/" + ($s.max_connections // 0 | tostring)),
       ($s.cache_hit_ratio // 0 | tostring)]
    ) | @tsv' | column -t -s $'\t'
  echo ""
else
  while IFS= read -r server; do
    STAGE=$(curl -sf "$GATEWAY/api/v1/fleet/jobs/$JOB_ID/servers/$server" \
      -H "$AUTH_HEADER" 2>/dev/null | jq -r '.stage // "-"') || STAGE="-"
    echo ""
    echo "  Server: $server  [$STAGE]"
    echo "  ──────────────────────────────────────────────────────────────────────"
    echo "$ALL_RESULTS" \
      | jq -r --arg s "$server" \
        '.[] | select(.server==$s) | .steps[] |
         "  [step \(.step_index)] \(.tool)  [\(.status // "?")]",
         ("  " + (.output // "(no output)"))'
  done <<<"$SERVERS"
  echo ""
fi

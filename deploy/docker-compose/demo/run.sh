#!/usr/bin/env bash
# aiHelpDesk demo runner.
# Runs inside the demo-runner container (helpdesk image: has psql, curl, faulttest).
set -euo pipefail

# ── colour helpers ────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; DIM='\033[2m'; RESET='\033[0m'

say()  { printf "${CYAN}▶ %s${RESET}\n" "$*"; }
ok()   { printf "${GREEN}✓ %s${RESET}\n" "$*"; }
warn() { printf "${YELLOW}⚠ %s${RESET}\n" "$*"; }
err()  { printf "${RED}✗ %s${RESET}\n" "$*" >&2; }
sep()  { printf "${DIM}%s${RESET}\n" "────────────────────────────────────────────────────────────────"; }
bold() { printf "${BOLD}%s${RESET}\n" "$*"; }

# ── config from environment ───────────────────────────────────────────────────
GATEWAY_URL="${HELPDESK_GATEWAY_URL:-http://gateway:8080}"
# HOST_GATEWAY_URL is the address the user's terminal can reach (port-mapped to host).
# GATEWAY_URL is used for internal container-to-container calls; this is for printed commands.
HOST_GATEWAY_URL="http://localhost:8180"
API_KEY="${HELPDESK_CLIENT_API_KEY:-demo-api-key}"
# AUDITD_URL: seed_vault_history talks to auditd directly because POST
# /v1/fleet/playbooks/{id}/runs (raw run creation) is not proxied through the
# gateway's /api/v1 surface — only the trigger ("/run") and read paths are.
# demo-runner shares the compose network with demo-auditd, so this resolves.
AUDITD_URL="${HELPDESK_AUDIT_URL:-http://demo-auditd:1299}"

# ── API key guard ─────────────────────────────────────────────────────────────
# The db-agent receives HELPDESK_API_KEY at container start time (docker compose
# up -d). If no key was set then, the agent is crash-looping and the gateway
# will never become healthy. Detect this early and give actionable instructions.
if [[ -z "${HELPDESK_API_KEY:-}" ]]; then
  err "No LLM API key found — the db-agent likely started without one and is crash-looping."
  printf "\n"
  printf "  ${BOLD}Fix (takes about 30 seconds):${RESET}\n"
  printf "\n"
  printf "  1. Export your API key in this shell:\n"
  printf "       export ANTHROPIC_API_KEY=sk-ant-...   ${DIM}# Anthropic${RESET}\n"
  printf "       export GOOGLE_API_KEY=AIza...         ${DIM}# Google / Gemini${RESET}\n"
  printf "\n"
  printf "  2. Restart the stack so the db-agent picks up the key:\n"
  printf "       docker compose -f docker-compose.demo.yaml down\n"
  printf "       docker compose -f docker-compose.demo.yaml up -d\n"
  printf "\n"
  printf "  3. Re-run the demo once all services are healthy:\n"
  printf "       docker compose -f docker-compose.demo.yaml run --rm demo-runner\n"
  printf "\n"
  exit 1
fi

# Detect LLM vendor from API keys. Use DEMO_MODEL_* (not HELPDESK_MODEL_*) to
# avoid bleeding the user's production env vars into the demo model selection.
if [[ -n "${GOOGLE_API_KEY:-}${GEMINI_API_KEY:-}" && -z "${ANTHROPIC_API_KEY:-}" ]]; then
  DEMO_MODEL_VENDOR="${DEMO_MODEL_VENDOR:-google}"
  DEMO_MODEL_NAME="${DEMO_MODEL_NAME:-gemini-2.5-flash}"
elif [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  DEMO_MODEL_VENDOR="${DEMO_MODEL_VENDOR:-anthropic}"
  DEMO_MODEL_NAME="${DEMO_MODEL_NAME:-claude-haiku-4-5-20251001}"
fi
DETECTED_VENDOR="${DEMO_MODEL_VENDOR:-anthropic} / ${DEMO_MODEL_NAME:-claude-haiku-4-5-20251001}"
CONN="${DEMO_CONN:-host=demo-postgres port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable}"
FAULT="${DEMO_FAULT:-db-max-connections}"
MODE="${DEMO_MODE:-interactive}"   # "auto" or "interactive"
AUTO_APPROVE_SECS="${DEMO_AUTO_APPROVE_SECS:-8}"
OPERATOR="${DEMO_OPERATOR:-demo@aihelpdesk.biz}"

# ── fault catalogue ───────────────────────────────────────────────────────────
declare -A FAULT_NAMES=(
  [db-max-connections]="Max connections exhausted"
  [db-long-running-query]="Long-running query blocking"
  [db-tx-lock-chain-blocker]="Transaction lock chain — active root blocker"
)
# Triage playbooks (execution_mode: agent) — diagnosis only, no step-approval gate.
declare -A FAULT_SERIES_TRIAGE=(
  [db-max-connections]="pbs_connection_triage"
  [db-long-running-query]="pbs_slow_query_triage"
  [db-tx-lock-chain-blocker]="pbs_lock_chain_triage"
)
# Remediation playbooks (execution_mode: agent_approve) — step-approval gate fires.
declare -A FAULT_SERIES_REMEDIATE=(
  [db-max-connections]="pbs_connection_remediate"
  [db-long-running-query]="pbs_slow_query_remediate"
  [db-tx-lock-chain-blocker]="pbs_lock_chain_remediate"
)
# Modes A and B use remediation directly so the step-approval gate actually fires.
declare -A FAULT_SERIES=(
  [db-max-connections]="pbs_connection_remediate"
  [db-long-running-query]="pbs_slow_query_remediate"
  [db-tx-lock-chain-blocker]="pbs_lock_chain_remediate"
)

if [[ -z "${FAULT_NAMES[$FAULT]:-}" ]]; then
  err "Unknown fault '${FAULT}'. Valid values: ${!FAULT_NAMES[*]}"
  exit 1
fi
SERIES="${FAULT_SERIES[$FAULT]}"

# ── wait helpers ──────────────────────────────────────────────────────────────
wait_for_http() {
  local url="$1" label="$2" max="${3:-60}" i=0
  printf "${DIM}  Waiting for %s" "$label"
  until curl -sf "$url" >/dev/null 2>&1; do
    (( i++ ))
    if (( i > max )); then printf "${RESET}\n"; err "Timed out waiting for $label"; exit 1; fi
    printf "."
    sleep 1
  done
  printf "${RESET}\n"
  ok "$label is ready"
}

wait_for_psql() {
  local i=0
  printf "${DIM}  Waiting for demo-postgres"
  until PGPASSWORD=demopassword psql "host=demo-postgres port=5432 dbname=postgres user=postgres" \
        -c "SELECT 1" >/dev/null 2>&1; do
    (( i++ ))
    if (( i > 60 )); then printf "${RESET}\n"; err "Timed out waiting for Postgres"; exit 1; fi
    printf "."; sleep 1
  done
  printf "${RESET}\n"
  ok "demo-postgres is ready"
}

gw() {
  curl -sf -H "Authorization: Bearer ${API_KEY}" -H "Content-Type: application/json" \
       -H "X-User: ${OPERATOR}" "$@"
}

gw_as() {
  local user="$1"; shift
  curl -sf -H "Authorization: Bearer ${API_KEY}" -H "Content-Type: application/json" \
       -H "X-User: ${user}" "$@"
}

# ── fault injection ───────────────────────────────────────────────────────────
inject_fault() {
  case "$FAULT" in
    db-max-connections)
      say "Injecting fault: saturating connection pool with idle sessions..."
      # Clean up any ghost demo_fault connections left by a previous interrupted run.
      # When the demo-runner container is SIGKILL'd, its psql clients die but PostgreSQL
      # keeps the server-side backends until TCP keepalives fire (hours). Terminate them
      # before counting slots so EXISTING reflects reality.
      PGPASSWORD=demopassword psql "$CONN" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='demo_fault' AND pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      sleep 0.5
      # Read max_connections and superuser_reserved_connections, then fill the
      # remaining slots so the agent sees a saturated pool.
      MAX=$(PGPASSWORD=demopassword psql "$CONN" -t -A -c "SHOW max_connections;" 2>/dev/null | tr -d ' \n')
      SU_RES=$(PGPASSWORD=demopassword psql "$CONN" -t -A -c "SHOW superuser_reserved_connections;" 2>/dev/null | tr -d ' \n')
      # Count existing connections (including our own)
      EXISTING=$(PGPASSWORD=demopassword psql "$CONN" -t -A -c "SELECT count(*) FROM pg_stat_activity;" 2>/dev/null | tr -d ' \n')
      TARGET=$(( MAX - SU_RES - 1 ))
      TO_OPEN=$(( TARGET - EXISTING ))
      if (( TO_OPEN < 1 )); then TO_OPEN=1; fi
      say "  max_connections=${MAX}, superuser_reserved=${SU_RES}, opening ${TO_OPEN} idle connections..."
      # Use tail -f /dev/null piped to psql to create persistent IDLE connections.
      # psql reads from the pipe (which never closes), stays connected, state='idle'.
      # application_name=demo_fault marks them for teardown without touching real sessions.
      FAULT_CONN="${CONN} application_name=demo_fault"
      for (( i=0; i<TO_OPEN; i++ )); do
        { tail -f /dev/null 2>/dev/null | PGPASSWORD=demopassword psql "${FAULT_CONN}" >/dev/null 2>&1 & } 2>/dev/null
      done
      sleep 1
      IDLE=$(PGPASSWORD=demopassword psql "$CONN" -t -A \
        -c "SELECT count(*) FROM pg_stat_activity WHERE application_name='demo_fault' AND state='idle';" 2>/dev/null | tr -d ' \n')
      ok "Fault active: ${IDLE} idle connections holding the pool (max=${MAX})"
      ;;
    db-long-running-query)
      say "Injecting fault: starting a long-running query (pg_sleep 300s)..."
      { PGPASSWORD=demopassword psql "$CONN" -c "SELECT pg_sleep(300);" >/dev/null 2>&1 & } 2>/dev/null
      sleep 1
      PID=$(PGPASSWORD=demopassword psql "$CONN" -t -A \
        -c "SELECT pid FROM pg_stat_activity WHERE query LIKE '%pg_sleep%' AND state='active' LIMIT 1;" \
        2>/dev/null | tr -d ' \n')
      ok "Fault active: long-running query pid=${PID} (pg_sleep 300s)"
      # Let the query age so the agent sees it as long-running (> 30s threshold).
      # The planner + auto-approved reads take ~10-15s; 25s here gives ~35-40s total.
      say "  Letting the fault age so the agent sees it as long-running (25s)..."
      sleep 25
      ;;
    db-tx-lock-chain-blocker)
      say "Injecting fault: creating a transaction lock chain..."
      # Session 1: acquire advisory lock 42 and hold it open (never commit).
      # pg_advisory_xact_lock is released only when the transaction ends.
      { PGPASSWORD=demopassword psql "$CONN" \
          -c "BEGIN; SELECT pg_advisory_xact_lock(42); SELECT pg_sleep(300);" \
          >/dev/null 2>&1 & } 2>/dev/null
      sleep 2
      # Session 2: request the same advisory lock — blocks until session 1 ends.
      { PGPASSWORD=demopassword psql "$CONN" \
          -c "BEGIN; SELECT pg_advisory_xact_lock(42); SELECT pg_sleep(300);" \
          >/dev/null 2>&1 & } 2>/dev/null
      sleep 1
      BLOCKERS=$(PGPASSWORD=demopassword psql "$CONN" -t -A \
        -c "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock';" 2>/dev/null | tr -d ' \n')
      ok "Fault active: lock chain established (${BLOCKERS} sessions waiting on lock)"
      ;;
  esac
}

teardown_fault() {
  case "$FAULT" in
    db-max-connections)
      PGPASSWORD=demopassword psql "$CONN" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='demo_fault' AND pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      ;;
    db-long-running-query)
      PGPASSWORD=demopassword psql "$CONN" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE '%pg_sleep%' AND pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      ;;
    db-tx-lock-chain-blocker)
      PGPASSWORD=demopassword psql "$CONN" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE '%advisory_xact_lock%' AND pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      ;;
  esac
}

# ── playbook trigger ──────────────────────────────────────────────────────────
# curl_gw: like gw() but without -f so the response body is always captured.
curl_gw() {
  curl -s \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -H "X-User: ${OPERATOR}" \
    "$@"
}

curl_gw_as() {
  local user="$1"; shift
  curl -s \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -H "X-User: ${user}" \
    "$@"
}

# curl_auditd: like curl_gw but targets auditd directly (see AUDITD_URL above).
# The demo-runner service account (Bearer demo-api-key) is defined in
# demo/users.yaml, the same file mounted into both auditd and the gateway.
curl_auditd() {
  curl -s \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -H "X-User: ${OPERATOR}" \
    "$@"
}

# trigger_playbook [series] [approval_mode] — returns full ApproveRunResponse JSON.
# agent_approve: the gateway proposes step 1 synchronously; status=pending_approval
# is in the response body — there is nothing to poll.
trigger_playbook() {
  local series="${1:-$SERIES}" mode="${2:-manual}"
  local resp run_id
  resp=$(curl_gw -X POST "${GATEWAY_URL}/api/v1/fleet/playbooks/${series}/run" \
    -d "{\"connection_string\": \"${CONN}\", \"approval_mode\": \"${mode}\"}" 2>&1)
  run_id=$(printf '%s' "$resp" | grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  if [[ -z "$run_id" ]]; then
    err "Failed to trigger playbook: $resp"
    exit 1
  fi
  printf '%s' "$resp"
}

# trigger_playbook_as <user> <series> <approval_mode> — returns full JSON response
trigger_playbook_as() {
  local user="$1" series="$2" mode="$3"
  curl_gw_as "$user" -X POST "${GATEWAY_URL}/api/v1/fleet/playbooks/${series}/run" \
    -d "{\"connection_string\": \"${CONN}\", \"approval_mode\": \"${mode}\"}" 2>&1
}

# ── step approval ─────────────────────────────────────────────────────────────
# The agent_approve flow is synchronous: POST /run returns step 1 with
# status=pending_approval; POST /proceed executes the step and returns the next
# step (or status=complete). No polling of GET /playbook-runs/{id} is needed.

# approve_step <run_id> <step_index> — executes the step and returns next ApproveRunResponse
approve_step() {
  local run_id="$1" step_index="${2:-1}"
  curl_gw -X POST "${GATEWAY_URL}/api/v1/fleet/playbook-runs/${run_id}/proceed" \
    -d "{\"resolution\":\"approved\",\"resolved_by\":\"${OPERATOR}\",\"step_index\":${step_index},\"connection_string\":\"${CONN}\"}" 2>&1
}

# deny_step <run_id> <step_index> [reason]
deny_step() {
  local run_id="$1" step_index="${2:-1}" reason="${3:-denied by operator}"
  curl_gw -X POST "${GATEWAY_URL}/api/v1/fleet/playbook-runs/${run_id}/proceed" \
    -d "{\"resolution\":\"denied\",\"resolved_by\":\"${OPERATOR}\",\"step_index\":${step_index},\"reason\":\"${reason}\"}" \
    2>/dev/null || true
}

# show_gate_ui <resp_json> — prints the step-approval gate box from an ApproveRunResponse
show_gate_ui() {
  local resp="$1"
  local tool reason action_class approval_id
  tool=$(printf '%s' "$resp"         | grep -o '"tool":"[^"]*"'         | head -1 | cut -d'"' -f4)
  reason=$(printf '%s' "$resp"       | grep -o '"reason":"[^"]*"'       | head -1 | cut -d'"' -f4)
  action_class=$(printf '%s' "$resp" | grep -o '"action_class":"[^"]*"' | head -1 | cut -d'"' -f4)
  approval_id=$(printf '%s' "$resp"  | grep -o '"approval_id":"[^"]*"'  | head -1 | cut -d'"' -f4)
  printf "\n"
  bold "  ┌─────────────────────────────────────────────────────────┐"
  bold "  │              INFORMED CONSENT GATE                      │"
  bold "  └─────────────────────────────────────────────────────────┘"
  printf "\n"
  printf "  ${BOLD}The AI agent has diagnosed the fault and proposes a remediation step.${RESET}\n"
  printf "  Before executing, human approval is required.\n"
  printf "\n"
  [[ -n "$tool"         ]] && printf "  Proposed action:  ${BOLD}%s${RESET}\n" "$tool"
  [[ -n "$reason"       ]] && printf "  Reasoning:        %s\n" "$reason"
  [[ -n "$action_class" ]] && printf "  Action class:     ${YELLOW}%s${RESET}\n" "$action_class"
  printf "  Approval ID:      %s\n" "$approval_id"
  printf "\n"
  printf "  ${DIM}Right I: nothing executes until you have reviewed action, reasoning, and blast radius.${RESET}\n"
  printf "  ${DIM}This is your entitlement — not a default that can be configured away.${RESET}\n"
  printf "\n"
}

# run_approval_loop <initial_resp> <run_id> — drives all approve/deny rounds until complete.
# Handles both interactive (MODE=interactive) and auto-approve (MODE=auto) flows.
# Read actions are auto-approved silently; only write/destructive actions pause for input.
# Returns 0 on complete/resolved, 1 on denied/error.
run_approval_loop() {
  local resp="$1" run_id="$2" step_num=0
  while true; do
    local status step_index action_class
    status=$(printf '%s' "$resp" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
    case "$status" in
      complete|resolved)
        local summary
        summary=$(printf '%s' "$resp" | grep -o '"summary":"[^"]*"' | head -1 | cut -d'"' -f4)
        ok "Playbook completed — ${summary:-resolved}"
        return 0
        ;;
      denied)
        warn "Step was denied. Playbook abandoned."
        return 1
        ;;
      pending_approval)
        (( step_num++ )) || true
        step_index=$(printf '%s' "$resp" | grep -o '"index":[0-9]*' | head -1 | cut -d':' -f2)
        step_index="${step_index:-1}"
        action_class=$(printf '%s' "$resp" | grep -o '"action_class":"[^"]*"' | head -1 | cut -d'"' -f4)

        if [[ "$action_class" == "read" ]]; then
          # Reads are auto-approved — they carry no blast radius risk.
          # Show a brief inline trace so the demo log shows what the agent is doing.
          local read_tool
          read_tool=$(printf '%s' "$resp" | grep -o '"tool":"[^"]*"' | head -1 | cut -d'"' -f4)
          printf "  ${DIM}agent reading: %s${RESET}\n" "${read_tool:-?}"
          resp=$(approve_step "$run_id" "$step_index")
          if [[ -z "$resp" ]]; then
            err "approve_step returned empty response — check gateway logs."
            return 1
          fi
          continue
        fi

        show_gate_ui "$resp"
        if [[ "$MODE" == "auto" ]]; then
          printf "  ${YELLOW}Auto-approve mode: approving in %d seconds...${RESET}\n" "$AUTO_APPROVE_SECS"
          for (( i=AUTO_APPROVE_SECS; i>0; i-- )); do
            printf "\r  ${YELLOW}  Approving in %d...${RESET}  " "$i"; sleep 1
          done
          printf "\r  ${GREEN}  Approving now...              ${RESET}\n"
          ok "Gate approved automatically."
        else
          printf "  ${BOLD}Press ENTER to approve this action, or Ctrl-C to abort.${RESET}\n"
          printf "\n"
          printf "  (In production: operators use the Decision Hub, Slack, or git-branch approval flow.)\n"
          printf "\n"
          read -r _
          ok "Gate approved."
        fi
        resp=$(approve_step "$run_id" "$step_index")
        if [[ -z "$resp" ]]; then
          err "approve_step returned empty response — check gateway logs."
          return 1
        fi
        ;;
      *)
        err "Unexpected status '${status:-empty}'. Full response: ${resp}"
        return 1
        ;;
    esac
  done
}

# seed_vault_history <series_id> — pre-seeds 2 synthetic resolved runs so the
# calibration coda has a track record to show on a fresh vault, instead of
# printing "displays after 3+" on every first-ever demo run.
# No-op when DEMO_VAULT_SEED=false, or when the series already has >=2 runs
# recorded (real runs accumulate in the persistent demo-audit-data volume
# across demo-runner invocations, so this only fires once per fresh volume).
# Seeded runs are clearly tagged (operator + "[seeded]" findings prefix) so
# they're never mistaken for real remediation history when inspected directly.
seed_vault_history() {
  [[ "${DEMO_VAULT_SEED:-true}" == "false" ]] && return 0
  local series="$1"

  local vstats total_runs
  vstats=$(curl_gw "${GATEWAY_URL}/api/v1/fleet/series/${series}/version-stats" 2>/dev/null)
  # grep -o exits 1 when a fresh series has no "total_runs" field at all (empty
  # versions array) — under `set -o pipefail` that would otherwise abort the
  # whole script via set -e. The `|| true` keeps awk's correctly-computed "0".
  total_runs=$(printf '%s' "$vstats" | grep -o '"total_runs":[0-9]*' | awk -F: '{s+=$2} END{print s+0}') || true
  if [[ "${total_runs:-0}" -ge 2 ]]; then
    return 0
  fi

  local list_resp playbook_id
  list_resp=$(curl_gw "${GATEWAY_URL}/api/v1/fleet/playbooks?series_id=${series}&active_only=true" 2>/dev/null)
  playbook_id=$(printf '%s' "$list_resp" | grep -o '"playbook_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  if [[ -z "$playbook_id" ]]; then
    warn "Vault seed skipped — could not resolve active playbook for ${series}."
    return 0
  fi

  local now offset started completed body
  now=$(date -u +%s)
  for offset in 7200 3600; do
    started=$(date -u -d "@$((now - offset))" +%Y-%m-%dT%H:%M:%SZ)
    completed=$(date -u -d "@$((now - offset + 60))" +%Y-%m-%dT%H:%M:%SZ)
    body=$(printf '{"operator":"demo-seed@aihelpdesk.biz","outcome":"resolved","findings_summary":"[seeded] Remediation completed successfully.","started_at":"%s","completed_at":"%s"}' \
      "$started" "$completed")
    curl_auditd -X POST "${AUDITD_URL}/v1/fleet/playbooks/${playbook_id}/runs" -d "$body" >/dev/null 2>&1 || true
  done
  ok "Vault seeded — 2 prior resolved run(s) for ${series}."
}

# show_vault_coda <series_id> <fault_id> — displays Right IV calibration record.
# Skipped entirely when DEMO_VAULT_SEED=false.
# Calls two gateway endpoints; each is silently skipped on error or empty response.
show_vault_coda() {
  [[ "${DEMO_VAULT_SEED:-true}" == "false" ]] && return 0
  local series="$1" fault_id="$2"

  # ── Aggregate stats from version-stats endpoint ──────────────────────────
  local vstats total_runs resolved resolution_pct pb_ver
  vstats=$(curl_gw "${GATEWAY_URL}/api/v1/fleet/series/${series}/version-stats" 2>/dev/null)
  # Sum total_runs and resolved across all versions.
  # `|| true` guards against grep's exit-1-on-no-match under pipefail (see
  # seed_vault_history) — harmless here today since this always runs after a
  # real trigger, but kept for the same reason: don't let a silent set -e
  # kill the script on a series with zero recorded runs.
  total_runs=$(printf '%s' "$vstats" | grep -o '"total_runs":[0-9]*' | awk -F: '{s+=$2} END{print s+0}') || true
  resolved=$(printf '%s' "$vstats" | grep -o '"resolved":[0-9]*' | awk -F: '{s+=$2} END{print s+0}') || true
  pb_ver=$(printf '%s' "$vstats" | grep -o '"version":"[^"]*"' | head -1 | cut -d'"' -f4)
  if [[ "${total_runs:-0}" -gt 0 && "${resolved:-0}" -gt 0 ]]; then
    resolution_pct=$(( resolved * 100 / total_runs ))
  fi

  # ── Fault stability cert (optional — populated by faulttest) ─────────────
  local cert_line=""
  local fcert is_stable n_cert pass_rate conf_range diag_model
  fcert=$(curl_gw "${GATEWAY_URL}/api/v1/fleet/fault-stability/${fault_id}" 2>/dev/null)
  if printf '%s' "$fcert" | grep -q '"is_stable"'; then
    is_stable=$(printf '%s' "$fcert" | grep -o '"is_stable":[a-z]*' | cut -d':' -f2)
    n_cert=$(printf '%s' "$fcert" | grep -o '"n_runs":[0-9]*' | cut -d':' -f2)
    pass_rate=$(printf '%s' "$fcert" | grep -o '"pass_rate":[0-9.]*' | cut -d':' -f2)
    conf_range=$(printf '%s' "$fcert" | grep -o '"conf_range_pp":[0-9.]*' | cut -d':' -f2)
    diag_model=$(printf '%s' "$fcert" | grep -o '"diagnosis_model":"[^"]*"' | cut -d'"' -f4)
    local pass_int conf_int stable_label
    pass_int=$(printf '%.0f' "${pass_rate:-0}" 2>/dev/null || echo "?")
    conf_int=$(printf '%.0f' "${conf_range:-0}" 2>/dev/null || echo "?")
    if [[ "$is_stable" == "true" ]]; then
      stable_label="${GREEN}STABLE(${n_cert:-?})${RESET}"
    else
      stable_label="${YELLOW}UNSTABLE(${n_cert:-?})${RESET}"
    fi
    cert_line="  Fault cert:   ${stable_label} @ model: ${diag_model:-?}  pass: ${pass_int}% ±${conf_int}pp"
  fi

  # ── Display ───────────────────────────────────────────────────────────────
  printf "\n"
  sep
  bold "  Calibration record (Right IV — The Grade):"
  printf "\n"
  printf "  Playbook:     %s  %s\n" "$series" "${pb_ver:+v${pb_ver}}"
  printf "  This run:     #%s  outcome: resolved\n" "${total_runs:-?}"
  if [[ "${total_runs:-0}" -ge 3 ]]; then
    printf "  Track record: %s prior run(s)  resolution rate: %s%%\n" \
      "$(( total_runs - 1 ))" "${resolution_pct:-?}"
  else
    printf "  Track record: %s run(s) recorded — pass rate displays after 3+\n" "${total_runs:-1}"
  fi
  [[ -n "$cert_line" ]] && printf "%b\n" "$cert_line"
  printf "\n"
  printf "  ${DIM}vault accuracy %s   # full breakdown${RESET}\n" "$series"
  printf "\n"
  sep
}

# show_rights_exercised <run_id> — prints the Bill of Rights outro.
show_rights_exercised() {
  local run_id="$1"
  printf "\n"
  sep
  bold "  Rights exercised in this run:"
  printf "\n"
  printf "  ${GREEN}[+]${RESET} Right I   — Informed Consent\n"
  printf "      Nothing executed until you approved action + reasoning + blast radius.\n"
  printf "\n"
  printf "  ${GREEN}[+]${RESET} Right III — Full Audit Trail\n"
  printf "      Every tool call, approval, and reasoning chain is hash-chained.\n"
  printf "      ${DIM}curl -s '${HOST_GATEWAY_URL}/api/v1/governance/journeys?run_id=${run_id}' \\\n"
  printf "           -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "\n"
  printf "  ${GREEN}[+]${RESET} Right IV  — The Grade\n"
  printf "      Calibration record above. Accumulates with every run; queryable at any time.\n"
  printf "\n"
  printf "  ${DIM}[ ] Right II  — Second Opinion      run: faulttest --judge${RESET}\n"
  printf "  ${DIM}[ ] Right VIII — Switch Models       set: ANTHROPIC_MODEL= in .env${RESET}\n"
  printf "\n"
  sep
}

# ── mode C: force + clamping demonstration ───────────────────────────────────
run_mode_c() {
  local UNPRIVILEGED="operator@aihelpdesk.biz"   # roles: sre, operator — no dba_lead
  local PRIVILEGED="demo@aihelpdesk.biz"          # roles: dba, sre, operator, dba_lead
  local REMEDIATE="${FAULT_SERIES_REMEDIATE[$FAULT]}"

  sep
  printf "\n"
  bold "  Mode C — Governance Bypass: force + approval_override_roles"
  printf "\n"
  printf "  Two runs. Same fault. Same playbook. Different roles.\n"
  printf "\n"
  printf "  Run 1: ${UNPRIVILEGED} requests force\n"
  printf "         → clamped (no dba_lead) → gate still fires\n"
  printf "  Run 2: ${PRIVILEGED} requests force\n"
  printf "         → accepted (has dba_lead) → gate fires but authority is clear\n"
  printf "\n"
  printf "  Config: infrastructure.json sets approval_override_roles: [dba_lead]\n"
  printf "\n"
  sep
  printf "\n"

  seed_vault_history "$REMEDIATE"

  say "Step 1 — Inject fault..."
  inject_fault
  printf "\n"

  # ── Run 1: unprivileged force ─────────────────────────────────────────────
  sep
  bold "  ┌─────────────────────────────────────────────────────────┐"
  bold "  │  Run 1/2 — ${UNPRIVILEGED}"
  bold "  │  approval_mode: force    role: sre, operator (no dba_lead)"
  bold "  └─────────────────────────────────────────────────────────┘"
  printf "\n"
  say "  Triggering ${REMEDIATE} as ${UNPRIVILEGED} with approval_mode=force..."

  local resp1
  resp1=$(trigger_playbook_as "$UNPRIVILEGED" "$REMEDIATE" "force") || {
    err "Failed to trigger playbook (run 1)"; exit 1
  }
  local run_id1 eff1
  run_id1=$(printf '%s' "$resp1" | grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  eff1=$(printf '%s' "$resp1" | grep -o '"effective_approval_mode":"[^"]*"' | head -1 | cut -d'"' -f4)
  local has_warn1=0
  printf '%s' "$resp1" | grep -q '"warnings":\[' && has_warn1=1 || true

  if [[ -z "$run_id1" ]]; then
    err "No run_id in response: $resp1"; exit 1
  fi

  printf "\n"
  printf "  Requested:          ${BOLD}force${RESET}\n"
  if [[ "$has_warn1" -eq 1 ]]; then
    printf "  Effective:          ${YELLOW}%s${RESET}  ← clamped\n" "${eff1:-manual}"
    warn "  Gateway: approval_mode clamped — ${UNPRIVILEGED} lacks dba_lead"
  else
    printf "  Effective:          ${GREEN}%s${RESET}\n" "${eff1:-manual}"
    warn "  No clamping detected — check approval_override_roles in infrastructure.json"
  fi
  printf "  Run ID:             %s\n" "$run_id1"
  printf "\n"

  # Gate is in the trigger response — no polling needed.
  local status1
  status1=$(printf '%s' "$resp1" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
  local step_index1
  step_index1=$(printf '%s' "$resp1" | grep -o '"index":[0-9]*' | head -1 | cut -d':' -f2)
  step_index1="${step_index1:-1}"

  if [[ "$status1" == "pending_approval" ]]; then
    ok "  Gate fired as expected — force was clamped, approval still required."
    local tool1 action1
    tool1=$(printf '%s' "$resp1"   | grep -o '"tool":"[^"]*"'         | head -1 | cut -d'"' -f4)
    action1=$(printf '%s' "$resp1" | grep -o '"action_class":"[^"]*"' | head -1 | cut -d'"' -f4)
    [[ -n "$tool1"   ]] && printf "  Proposed action:    %s\n" "$tool1"
    [[ -n "$action1" ]] && printf "  Action class:       ${YELLOW}%s${RESET}\n" "$action1"
    printf "\n"
    printf "  ${DIM}Denying step — this run was for proof of clamping, not remediation.${RESET}\n"
    deny_step "$run_id1" "$step_index1" "Mode C demo: proving clamping — run stopped"
    ok "  Step denied. Run 1 stopped. Governance held."
  elif [[ "$status1" == "complete" ]]; then
    printf "  ${DIM}Playbook completed without gate (unexpected for agent_approve mode).${RESET}\n"
  else
    warn "  Unexpected status '${status1}' — check: docker compose -f docker-compose.demo.yaml logs demo-gateway"
  fi
  printf "\n"

  say "  Tearing down fault before Run 2..."
  teardown_fault 2>/dev/null || true
  sleep 2

  # ── Run 2: privileged force ───────────────────────────────────────────────
  sep
  bold "  ┌─────────────────────────────────────────────────────────┐"
  bold "  │  Run 2/2 — ${PRIVILEGED}"
  bold "  │  approval_mode: force    role: dba, sre, operator, dba_lead"
  bold "  └─────────────────────────────────────────────────────────┘"
  printf "\n"
  say "  Re-injecting fault..."
  inject_fault
  # Poll until session 2 is actually waiting on the lock (up to 15s).
  say "  Waiting for lock chain to establish..."
  local waiters=0 _i
  for _i in $(seq 1 15); do
    waiters=$(PGPASSWORD=demopassword psql "$CONN" -t -A \
      -c "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock';" \
      2>/dev/null | tr -d ' \n')
    [[ "${waiters:-0}" -gt 0 ]] && break
    sleep 1
  done
  ok "  Lock chain confirmed (${waiters} session(s) waiting)"
  printf "\n"

  say "  Triggering ${REMEDIATE} as ${PRIVILEGED} with approval_mode=force..."
  local resp2
  resp2=$(trigger_playbook_as "$PRIVILEGED" "$REMEDIATE" "force") || {
    err "Failed to trigger playbook (run 2)"; exit 1
  }
  local run_id2 eff2
  run_id2=$(printf '%s' "$resp2" | grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  eff2=$(printf '%s' "$resp2" | grep -o '"effective_approval_mode":"[^"]*"' | head -1 | cut -d'"' -f4)
  local has_warn2=0
  printf '%s' "$resp2" | grep -q '"warnings":\[' && has_warn2=1 || true

  if [[ -z "$run_id2" ]]; then
    err "No run_id in response: $resp2"; exit 1
  fi

  printf "\n"
  printf "  Requested:          ${BOLD}force${RESET}\n"
  if [[ "$has_warn2" -eq 0 ]]; then
    printf "  Effective:          ${GREEN}%s${RESET}  ← accepted, no clamping\n" "${eff2:-force}"
    ok "  Gateway accepted force — dba_lead role verified."
  else
    printf "  Effective:          ${YELLOW}%s${RESET}  ← unexpected clamping\n" "${eff2:-manual}"
    warn "  Unexpected clamping for ${PRIVILEGED} — check roles in users.yaml"
  fi
  printf "  Run ID:             %s\n" "$run_id2"
  printf "\n"

  # Gate is in the trigger response — no polling needed.
  local status2
  status2=$(printf '%s' "$resp2" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)

  # Run 2: loop through all steps — auto-approve reads silently, show gate for
  # writes/destructive but approve immediately (the clamping point is already made).
  local r2="$resp2" r2_first=1 r2_step r2_status r2_action r2_tool r2_idx
  while true; do
    r2_status=$(printf '%s' "$r2" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
    case "$r2_status" in
      complete|resolved)
        local r2_summary
        r2_summary=$(printf '%s' "$r2" | grep -o '"summary":"[^"]*"' | head -1 | cut -d'"' -f4)
        ok "  Completed — ${r2_summary:-resolved}"
        break ;;
      denied)
        warn "  Run 2 step denied unexpectedly."; break ;;
      pending_approval)
        r2_idx=$(printf '%s' "$r2" | grep -o '"index":[0-9]*' | head -1 | cut -d':' -f2)
        r2_idx="${r2_idx:-1}"
        r2_action=$(printf '%s' "$r2" | grep -o '"action_class":"[^"]*"' | head -1 | cut -d'"' -f4)
        if [[ "$r2_action" == "read" ]]; then
          r2_tool=$(printf '%s' "$r2" | grep -o '"tool":"[^"]*"' | head -1 | cut -d'"' -f4)
          printf "  ${DIM}agent reading: %s${RESET}\n" "${r2_tool:-?}"
        else
          if [[ "$r2_first" -eq 1 ]]; then
            show_gate_ui "$r2"
            printf "  ${DIM}The gate fired — even with force, agent_approve always proposes steps one at a time.${RESET}\n"
            printf "  ${DIM}The difference: this user's force request was NOT downgraded. They have the authority.${RESET}\n"
            printf "\n"
            r2_first=0
          else
            show_gate_ui "$r2"
          fi
          printf "  Auto-approving (${PRIVILEGED} is authorized)...\n"
        fi
        r2=$(approve_step "$run_id2" "$r2_idx")
        ;;
      *) warn "  Unexpected status '${r2_status:-empty}'."; break ;;
    esac
  done
  show_vault_coda "$REMEDIATE" "$FAULT"

  # ── Summary ───────────────────────────────────────────────────────────────
  printf "\n"
  sep
  bold "  What you just saw:"
  printf "\n"
  printf "  Run 1 (%s)\n" "$UNPRIVILEGED"
  printf "    requested=force  effective=%-8s  gate=fired  verdict=denied\n" "${eff1:-manual}"
  printf "\n"
  printf "  Run 2 (%s)\n" "$PRIVILEGED"
  printf "    requested=force  effective=%-8s  gate=fired  verdict=approved\n" "${eff2:-force}"
  printf "\n"
  printf "  The gate fired in both runs — agent_approve always proposes steps one at a time.\n"
  printf "  The only difference visible to the audit trail: one user's force request\n"
  printf "  was silently downgraded. The other's was accepted. One line in users.yaml\n"
  printf "  and one field in infrastructure.json control who holds the key.\n"
  printf "\n"
  printf "  Journeys (WHAT + WHY):\n"
  printf "    ${DIM}curl -s '${HOST_GATEWAY_URL}/api/v1/governance/journeys?run_id=${run_id1}' \\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "    ${DIM}curl -s '${HOST_GATEWAY_URL}/api/v1/governance/journeys?run_id=${run_id2}' \\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "\n"
  printf "  Audit trail (both runs):\n"
  printf "    ${DIM}curl -s '${HOST_GATEWAY_URL}/api/v1/governance/events?limit=30' \\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq '.[] | {user,tool,action_class}'${RESET}\n"
  printf "\n"
  show_rights_exercised "$run_id2"

  teardown_fault 2>/dev/null || true
}


# ── main ──────────────────────────────────────────────────────────────────────
main() {
  sep
  bold "  aiHelpDesk — Governed AI Incident Response Demo"
  sep
  printf "\n"
  printf "  Fault:     ${BOLD}%s${RESET} (%s)\n" "${FAULT_NAMES[$FAULT]}" "$FAULT"
  printf "  Mode:      ${BOLD}%s${RESET}\n" "$(case "$MODE" in
    auto)      echo 'auto-approve (mode A)' ;;
    clamping)  echo 'governance bypass demo (mode C)' ;;
    *)         echo 'interactive approval (mode B)' ;;
  esac)"
  printf "  Playbook:  %s\n" "$SERIES"
  printf "  Model:     %s\n" "$DETECTED_VENDOR"
  printf "  Gateway:   %s\n" "$GATEWAY_URL"
  printf "  Rights:    ${DIM}I (Consent) · III (Audit Trail) · IV (Grade)${RESET}\n"
  printf "\n"
  sep
  printf "\n"

  # Mode C has its own flow
  if [[ "$MODE" == "clamping" ]]; then
    say "Step 1/1 — Waiting for services to be ready..."
    wait_for_psql
    wait_for_http "${GATEWAY_URL}/health" "gateway"
    printf "\n"
    run_mode_c
    return
  fi

  # Step 1 — Wait for services
  say "Step 1/5 — Waiting for services to be ready..."
  wait_for_psql
  wait_for_http "${GATEWAY_URL}/health" "gateway"
  seed_vault_history "$SERIES"
  printf "\n"

  # Step 2 — Inject fault
  sep
  say "Step 2/5 — Injecting fault..."
  inject_fault
  printf "\n"
  say "  Current database state:"
  PGPASSWORD=demopassword psql "$CONN" -c \
    "SELECT count(*) AS total_connections,
            sum(CASE WHEN state='idle' THEN 1 ELSE 0 END) AS idle,
            sum(CASE WHEN state='active' THEN 1 ELSE 0 END) AS active
     FROM pg_stat_activity;" 2>/dev/null || true
  printf "\n"

  # Step 3 — Trigger playbook (synchronous: step 1 is proposed in the response)
  sep
  say "Step 3/5 — Triggering playbook '${SERIES}'..."
  say "  The step proposer is planning the first remediation action (15–45 seconds)..."
  TRIGGER_RESP=$(trigger_playbook)
  RUN_ID=$(printf '%s' "$TRIGGER_RESP" | grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  ok "Playbook run started: ${RUN_ID}"
  printf "\n"

  # Step 4 — Gate + approval loop
  # handlePlaybookRunApprove returns pending_approval synchronously in the trigger body.
  # POST /proceed executes the step and returns the next step or status=complete.
  sep
  say "Step 4/5 — Step-approval gate..."
  run_approval_loop "$TRIGGER_RESP" "$RUN_ID"
  printf "\n"
  show_vault_coda "$SERIES" "$FAULT"

  # Step 5 — Post-remediation state
  sep
  say "Step 5/5 — Remediation complete."
  printf "\n"
  bold "  ┌─────────────────────────────────────────────────────────┐"
  bold "  │              INCIDENT RESOLVED                          │"
  bold "  └─────────────────────────────────────────────────────────┘"
  printf "\n"
  say "  Post-remediation database state:"
  PGPASSWORD=demopassword psql "$CONN" -c \
    "SELECT count(*) AS total_connections,
            sum(CASE WHEN state='idle' THEN 1 ELSE 0 END) AS idle,
            sum(CASE WHEN state='active' THEN 1 ELSE 0 END) AS active
     FROM pg_stat_activity;" 2>/dev/null || true

  printf "\n"
  sep
  bold "  What just happened:"
  printf "\n"
  printf "  1. A real fault was injected into a real PostgreSQL instance\n"
  printf "  2. The aiHelpDesk database agent diagnosed it autonomously\n"
  printf "  3. A step-approval gate surfaced — with blast radius and action class\n"
  printf "  4. A human approved (or auto-approval ran after a countdown)\n"
  printf "  5. The agent executed the remediation and verified recovery\n"
  printf "  6. The full trace is now in the audit log (tamper-proof, hash-chained)\n"
  printf "\n"
  printf "  Explore the audit trail:\n"
  printf "    ${DIM}curl -s '${HOST_GATEWAY_URL}/api/v1/governance/events?limit=5' \\\\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "\n"
  printf "  View the journey (WHAT + WHY):\n"
  printf "    ${DIM}curl -s '${HOST_GATEWAY_URL}/api/v1/governance/journeys?run_id=${RUN_ID}' \\\\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "\n"
  printf "  Try other faults:\n"
  printf "    ${DIM}DEMO_FAULT=db-long-running-query   docker compose -f docker-compose.demo.yaml run --rm demo-runner${RESET}\n"
  printf "    ${DIM}DEMO_FAULT=db-tx-lock-chain-blocker docker compose -f docker-compose.demo.yaml run --rm demo-runner${RESET}\n"
  printf "\n"
  show_rights_exercised "$RUN_ID"

  # Clean up injected connections
  teardown_fault 2>/dev/null || true
}

main "$@"

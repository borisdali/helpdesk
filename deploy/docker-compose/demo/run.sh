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
API_KEY="${HELPDESK_CLIENT_API_KEY:-demo-api-key}"

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

# Detect LLM vendor from available API keys when not explicitly set.
# Compose passes HELPDESK_MODEL_VENDOR/NAME with Anthropic defaults; if the
# operator set GOOGLE_API_KEY or GEMINI_API_KEY instead, override here.
if [[ -n "${GOOGLE_API_KEY:-}${GEMINI_API_KEY:-}" && -z "${ANTHROPIC_API_KEY:-}" ]]; then
  export HELPDESK_MODEL_VENDOR="${HELPDESK_MODEL_VENDOR:-google}"
  export HELPDESK_MODEL_NAME="${HELPDESK_MODEL_NAME:-gemini-2.5-flash}"
  export HELPDESK_API_KEY="${HELPDESK_API_KEY:-${GOOGLE_API_KEY:-${GEMINI_API_KEY:-}}}"
  DETECTED_VENDOR="Google (gemini-2.5-flash)"
elif [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  export HELPDESK_MODEL_VENDOR="${HELPDESK_MODEL_VENDOR:-anthropic}"
  export HELPDESK_MODEL_NAME="${HELPDESK_MODEL_NAME:-claude-haiku-4-5-20251001}"
  export HELPDESK_API_KEY="${HELPDESK_API_KEY:-${ANTHROPIC_API_KEY}}"
  DETECTED_VENDOR="Anthropic (claude-haiku-4-5-20251001)"
else
  DETECTED_VENDOR="${HELPDESK_MODEL_VENDOR:-anthropic} / ${HELPDESK_MODEL_NAME:-claude-haiku-4-5-20251001}"
fi
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
      for (( i=0; i<TO_OPEN; i++ )); do
        { PGPASSWORD=demopassword psql "$CONN" -c "SELECT pg_sleep(300);" >/dev/null 2>&1 & } 2>/dev/null
      done
      sleep 1
      ACTIVE=$(PGPASSWORD=demopassword psql "$CONN" -t -A \
        -c "SELECT count(*) FROM pg_stat_activity WHERE state='idle';" 2>/dev/null | tr -d ' \n')
      ok "Fault active: ${ACTIVE} idle connections holding the pool (max=${MAX})"
      ;;
    db-long-running-query)
      say "Injecting fault: starting a long-running query (pg_sleep 300s)..."
      { PGPASSWORD=demopassword psql "$CONN" -c "SELECT pg_sleep(300);" >/dev/null 2>&1 & } 2>/dev/null
      sleep 1
      PID=$(PGPASSWORD=demopassword psql "$CONN" -t -A \
        -c "SELECT pid FROM pg_stat_activity WHERE query LIKE '%pg_sleep%' AND state='active' LIMIT 1;" \
        2>/dev/null | tr -d ' \n')
      ok "Fault active: long-running query pid=${PID} (pg_sleep 300s)"
      ;;
    db-tx-lock-chain-blocker)
      say "Injecting fault: creating a transaction lock chain..."
      # Session 1: hold a lock by leaving a transaction open
      { PGPASSWORD=demopassword psql "$CONN" <<'SQL' >/dev/null 2>&1 &
BEGIN;
CREATE TABLE IF NOT EXISTS demo_lock_target (id int);
INSERT INTO demo_lock_target VALUES (1);
SELECT pg_sleep(300);
SQL
      } 2>/dev/null
      sleep 2
      # Session 2: try to lock the same table (will block)
      { PGPASSWORD=demopassword psql "$CONN" \
          -c "BEGIN; LOCK TABLE demo_lock_target IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(300);" \
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
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE state='idle' AND pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      ;;
    db-long-running-query)
      PGPASSWORD=demopassword psql "$CONN" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE '%pg_sleep%' AND pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      ;;
    db-tx-lock-chain-blocker)
      PGPASSWORD=demopassword psql "$CONN" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid();" \
        >/dev/null 2>&1 || true
      PGPASSWORD=demopassword psql "$CONN" \
        -c "DROP TABLE IF EXISTS demo_lock_target;" \
        >/dev/null 2>&1 || true
      ;;
  esac
}

# ── playbook trigger ──────────────────────────────────────────────────────────
# trigger_playbook [series] [approval_mode] — uses OPERATOR identity via gw()
trigger_playbook() {
  local series="${1:-$SERIES}" mode="${2:-review}"
  local resp run_id
  resp=$(gw -X POST "${GATEWAY_URL}/api/v1/fleet/playbooks/${series}/run" \
    -d "{\"connection_string\": \"${CONN}\", \"approval_mode\": \"${mode}\"}" 2>&1) || {
    err "Failed to trigger playbook: $resp"
    exit 1
  }
  run_id=$(printf '%s' "$resp" | grep -o '"run_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  if [[ -z "$run_id" ]]; then
    err "No run_id in response: $resp"
    exit 1
  fi
  printf '%s' "$run_id"
}

# trigger_playbook_as <user> <series> <approval_mode> — returns full JSON response
trigger_playbook_as() {
  local user="$1" series="$2" mode="$3"
  gw_as "$user" -X POST "${GATEWAY_URL}/api/v1/fleet/playbooks/${series}/run" \
    -d "{\"connection_string\": \"${CONN}\", \"approval_mode\": \"${mode}\"}" 2>&1
}

# ── poll for gate ─────────────────────────────────────────────────────────────
poll_for_gate() {
  local run_id="$1" i=0
  say "  Polling for step-approval gate..."
  while (( i < 120 )); do
    local resp status approval_id
    resp=$(gw "${GATEWAY_URL}/api/v1/fleet/playbook-runs/${run_id}" 2>/dev/null) || { sleep 2; (( i+=2 )); continue; }
    status=$(printf '%s' "$resp" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
    case "$status" in
      pending_approval|gate_pending)
        approval_id=$(printf '%s' "$resp" | grep -o '"approval_id":"[^"]*"' | head -1 | cut -d'"' -f4)
        # Extract the gate details for display
        local gate_summary action_class blast_radius
        gate_summary=$(printf '%s' "$resp" | grep -o '"summary":"[^"]*"' | head -1 | cut -d'"' -f4)
        action_class=$(printf '%s' "$resp" | grep -o '"action_class":"[^"]*"' | head -1 | cut -d'"' -f4)
        blast_radius=$(printf '%s' "$resp" | grep -o '"blast_radius":[0-9]*' | head -1 | cut -d':' -f2)
        printf '%s|%s|%s|%s|%s' "$approval_id" "$gate_summary" "$action_class" "$blast_radius" "$status"
        return 0
        ;;
      completed|resolved)
        printf 'COMPLETED||||||'
        return 0
        ;;
      failed|error)
        printf 'FAILED||||||'
        return 0
        ;;
    esac
    sleep 2; (( i+=2 ))
  done
  printf 'TIMEOUT||||||'
}

# ── approval ──────────────────────────────────────────────────────────────────
approve_gate() {
  local approval_id="$1"
  gw -X POST "${GATEWAY_URL}/api/v1/fleet/approvals/${approval_id}/approve" \
    -d "{\"operator\": \"${OPERATOR}\", \"verdict_notes\": \"Demo: approved via demo runner\"}" \
    >/dev/null 2>&1
}

deny_gate() {
  local run_id="$1" step_index="${2:-1}"
  gw -X POST "${GATEWAY_URL}/api/v1/fleet/playbook-runs/${run_id}/proceed" \
    -d "{\"resolution\":\"denied\",\"resolved_by\":\"${OPERATOR}\",\"step_index\":${step_index},\"reason\":\"Mode C demo: proving clamping — run stopped, re-running as privileged user\"}" \
    >/dev/null 2>&1 || true
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

  say "  Polling for step-approval gate (should fire — clamping kept approval required)..."
  local gate1 appr1
  gate1=$(poll_for_gate "$run_id1")
  appr1=$(printf '%s' "$gate1" | cut -d'|' -f1)
  local action1 summary1
  action1=$(printf '%s' "$gate1" | cut -d'|' -f3)
  summary1=$(printf '%s' "$gate1" | cut -d'|' -f2)

  if [[ "$appr1" == "COMPLETED" ]]; then
    printf "  ${DIM}Playbook completed without gate (unexpected for agent_approve mode).${RESET}\n"
  elif [[ "$appr1" == "FAILED" || "$appr1" == "TIMEOUT" ]]; then
    warn "  Run timed out or failed — check: docker compose --profile demo logs demo-gateway"
  else
    ok "  Gate fired as expected — force was clamped, approval still required."
    [[ -n "$summary1"   ]] && printf "  Proposed action:    %s\n" "$summary1"
    [[ -n "$action1"    ]] && printf "  Action class:       ${YELLOW}%s${RESET}\n" "$action1"
    printf "\n"
    printf "  ${DIM}Denying step — this run was for proof of clamping, not remediation.${RESET}\n"
    deny_gate "$run_id1" 1
    ok "  Step denied. Run 1 stopped. Governance held."
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

  say "  Polling for step-approval gate..."
  local gate2 appr2
  gate2=$(poll_for_gate "$run_id2")
  appr2=$(printf '%s' "$gate2" | cut -d'|' -f1)
  local action2 summary2 blast2
  summary2=$(printf '%s' "$gate2" | cut -d'|' -f2)
  action2=$(printf '%s' "$gate2" | cut -d'|' -f3)
  blast2=$(printf '%s' "$gate2" | cut -d'|' -f4)

  if [[ "$appr2" == "COMPLETED" ]]; then
    ok "  Playbook completed (agent_approve loop finished without pending steps)."
  elif [[ "$appr2" == "FAILED" || "$appr2" == "TIMEOUT" ]]; then
    warn "  Run timed out — check: docker compose --profile demo logs demo-gateway"
  else
    printf "\n"
    bold "  ┌─────────────────────────────────────────────────────────┐"
    bold "  │              STEP APPROVAL GATE                         │"
    bold "  └─────────────────────────────────────────────────────────┘"
    printf "\n"
    [[ -n "$summary2"  ]] && printf "  Proposed action:    ${BOLD}%s${RESET}\n" "$summary2"
    [[ -n "$action2"   ]] && printf "  Action class:       ${YELLOW}%s${RESET}\n" "$action2"
    [[ -n "$blast2"    ]] && printf "  Blast radius:       %s connections\n" "$blast2"
    printf "\n"
    printf "  ${DIM}The gate fired — even with force, agent_approve always proposes steps one at a time.${RESET}\n"
    printf "  ${DIM}The difference: this user's force request was NOT downgraded. They have the authority.${RESET}\n"
    printf "\n"
    printf "  Auto-approving (${PRIVILEGED} is authorized)...\n"
    approve_gate "$appr2"
    ok "  Step approved. Waiting for completion..."
    printf "\n"
    if FINAL2=$(poll_for_completion "$run_id2"); then
      OUTCOME2=$(printf '%s' "$FINAL2" | grep -o '"outcome":"[^"]*"' | head -1 | cut -d'"' -f4)
      ok "  Completed — outcome: ${OUTCOME2:-resolved}"
    else
      warn "  Run did not complete in expected window."
    fi
  fi

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
  printf "  Audit trail (both runs):\n"
  printf "    ${DIM}curl -s '${GATEWAY_URL}/api/v1/audit/events?limit=30' \\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq '.[] | {user,tool,action_class}'${RESET}\n"
  printf "\n"
  sep

  teardown_fault 2>/dev/null || true
}

poll_for_completion() {
  local run_id="$1" i=0
  while (( i < 120 )); do
    local resp status
    resp=$(gw "${GATEWAY_URL}/api/v1/fleet/playbook-runs/${run_id}" 2>/dev/null) || { sleep 2; (( i+=2 )); continue; }
    status=$(printf '%s' "$resp" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
    case "$status" in
      completed|resolved) printf '%s' "$resp"; return 0 ;;
      failed|error)       printf '%s' "$resp"; return 1 ;;
    esac
    sleep 2; (( i+=2 ))
  done
  return 1
}

# ── main ──────────────────────────────────────────────────────────────────────
main() {
  clear
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

  # Step 3 — Trigger playbook
  sep
  say "Step 3/5 — Triggering playbook '${SERIES}'..."
  RUN_ID=$(trigger_playbook)
  ok "Playbook run started: ${RUN_ID}"
  say "  The agent is now diagnosing the fault. This takes 15–45 seconds..."
  printf "\n"

  # Step 4 — Gate
  sep
  say "Step 4/5 — Waiting for step-approval gate..."
  printf "\n"
  GATE_RESULT=$(poll_for_gate "$RUN_ID")
  APPROVAL_ID=$(printf '%s' "$GATE_RESULT" | cut -d'|' -f1)
  GATE_SUMMARY=$(printf '%s' "$GATE_RESULT" | cut -d'|' -f2)
  ACTION_CLASS=$(printf '%s' "$GATE_RESULT" | cut -d'|' -f3)
  BLAST_RADIUS=$(printf '%s' "$GATE_RESULT" | cut -d'|' -f4)

  if [[ "$APPROVAL_ID" == "COMPLETED" ]]; then
    ok "Playbook completed without a gate (approval_mode may be set to auto)."
  elif [[ "$APPROVAL_ID" == "FAILED" || "$APPROVAL_ID" == "TIMEOUT" ]]; then
    err "Playbook did not reach a gate in time. Check gateway logs."
    exit 1
  else
    printf "\n"
    bold "  ┌─────────────────────────────────────────────────────────┐"
    bold "  │              STEP APPROVAL GATE                         │"
    bold "  └─────────────────────────────────────────────────────────┘"
    printf "\n"
    printf "  ${BOLD}The AI agent has diagnosed the fault and proposes a remediation step.${RESET}\n"
    printf "  Before executing, human approval is required.\n"
    printf "\n"
    [[ -n "$GATE_SUMMARY"   ]] && printf "  Proposed action:  ${BOLD}%s${RESET}\n" "$GATE_SUMMARY"
    [[ -n "$ACTION_CLASS"   ]] && printf "  Action class:     ${YELLOW}%s${RESET}\n" "$ACTION_CLASS"
    [[ -n "$BLAST_RADIUS"   ]] && printf "  Blast radius:     %s connections\n" "$BLAST_RADIUS"
    printf "  Approval ID:      %s\n" "$APPROVAL_ID"
    printf "\n"
    printf "  ${DIM}This is aiHelpDesk's L2 autonomy gate — the agent proposed the action,${RESET}\n"
    printf "  ${DIM}but nothing executes until a human approves it.${RESET}\n"
    printf "\n"

    if [[ "$MODE" == "auto" ]]; then
      printf "  ${YELLOW}Auto-approve mode: approving in %d seconds...${RESET}\n" "$AUTO_APPROVE_SECS"
      for (( i=AUTO_APPROVE_SECS; i>0; i-- )); do
        printf "\r  ${YELLOW}  Approving in %d...${RESET}  " "$i"
        sleep 1
      done
      printf "\r  ${GREEN}  Approving now...              ${RESET}\n"
      approve_gate "$APPROVAL_ID"
      ok "Gate approved automatically."
    else
      printf "  ${BOLD}Press ENTER to approve this action, or Ctrl-C to abort.${RESET}\n"
      printf "\n"
      printf "  (In production: operators use ${DIM}docker compose exec auditd approvals approve %s${RESET})\n" "$APPROVAL_ID"
      printf "  (Or via Slack notification, webhook, or the git-branch approval flow.)\n"
      printf "\n"
      read -r _
      approve_gate "$APPROVAL_ID"
      ok "Gate approved."
    fi
  fi
  printf "\n"

  # Step 5 — Resolution
  sep
  say "Step 5/5 — Waiting for playbook to complete..."
  printf "\n"
  if FINAL=$(poll_for_completion "$RUN_ID"); then
    OUTCOME=$(printf '%s' "$FINAL" | grep -o '"outcome":"[^"]*"' | head -1 | cut -d'"' -f4)
    printf "\n"
    bold "  ┌─────────────────────────────────────────────────────────┐"
    bold "  │              INCIDENT RESOLVED                          │"
    bold "  └─────────────────────────────────────────────────────────┘"
    printf "\n"
    ok "Playbook completed — outcome: ${OUTCOME:-resolved}"
    printf "\n"
    say "  Post-remediation database state:"
    PGPASSWORD=demopassword psql "$CONN" -c \
      "SELECT count(*) AS total_connections,
              sum(CASE WHEN state='idle' THEN 1 ELSE 0 END) AS idle,
              sum(CASE WHEN state='active' THEN 1 ELSE 0 END) AS active
       FROM pg_stat_activity;" 2>/dev/null || true
  else
    warn "Playbook did not complete within the expected window. Check gateway logs."
    warn "  docker compose logs gateway"
  fi

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
  printf "    ${DIM}curl -s ${GATEWAY_URL}/api/v1/audit/events?limit=5 \\\\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "\n"
  printf "  View the journey (WHAT + WHY):\n"
  printf "    ${DIM}curl -s '${GATEWAY_URL}/api/v1/audit/journeys?run_id=${RUN_ID}' \\\\\n"
  printf "         -H 'Authorization: Bearer ${API_KEY}' | jq .${RESET}\n"
  printf "\n"
  printf "  Try other faults:\n"
  printf "    ${DIM}DEMO_FAULT=db-long-running-query   docker compose --profile demo run --rm demo-runner${RESET}\n"
  printf "    ${DIM}DEMO_FAULT=db-tx-lock-chain-blocker docker compose --profile demo run --rm demo-runner${RESET}\n"
  printf "\n"
  sep

  # Clean up injected connections
  teardown_fault 2>/dev/null || true
}

main "$@"

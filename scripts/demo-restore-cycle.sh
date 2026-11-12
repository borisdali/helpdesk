#!/usr/bin/env bash
# One full point-in-time restore cycle against the local pgBackRest Docker fixture,
# started and approved like an operator would, so the incident is a real-origin one
# (a faulttest run is not counted by the availability ledger or the recovery-time
# estimate). Run it from the repo root; run it again for the next cycle.
#
#   prepare   full backup, a row before the target, the target, rows after it
#   plan      the read-only plan (rpo check, budget check, recovery estimate)
#   triage    pbs_pgbackrest_pitr_triage through the gateway, holding at the gate
#   approve   helpdesk-client --pitr-approve with the digest of the plan above
#   wait      until the incident leaves "open"
#   verify    the rows after the target are gone, the row before it is back
#   report    the incident narrative and the availability budget
#
# Destructive for the fixture only: it stops and restores the container.
#
# Needs: docker, curl, jq, go; the fixture up
#   docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait
# and the stack's infra entry for the fixture (default pgbackrest-db) in the file the
# gateway and the sysadmin agent were started with.
#
# Environment (the names helpdesk-client already uses):
#   HELPDESK_GATEWAY_URL     gateway as seen from this shell, e.g. http://localhost:8080
#   HELPDESK_CLIENT_API_KEY  a key that may start playbooks and approve gates
#   HELPDESK_SYSADMIN_URL    sysadmin agent as seen from this shell, e.g. http://localhost:1103
#   HELPDESK_SYSADMIN_KEY    its key (HELPDESK_SYSADMIN_API_KEY is accepted too)
# Optional:
#   HELPDESK_CLIENT_USER     the approver (default alice@example.com)
#   FIXTURE_CONTAINER        default helpdesk-test-pg-pgbackrest
#   FIXTURE_DB_KEY           infra key of the fixture, default pgbackrest-db
#   YES=1                    do not ask before starting
set -euo pipefail

GW=${HELPDESK_GATEWAY_URL:?set HELPDESK_GATEWAY_URL}
KEY=${HELPDESK_CLIENT_API_KEY:?set HELPDESK_CLIENT_API_KEY}
SYSADMIN=${HELPDESK_SYSADMIN_URL:?set HELPDESK_SYSADMIN_URL}
SYSADMIN_KEY=${HELPDESK_SYSADMIN_KEY:-${HELPDESK_SYSADMIN_API_KEY:?set HELPDESK_SYSADMIN_KEY}}
APPROVER=${HELPDESK_CLIENT_USER:-alice@example.com}
C=${FIXTURE_CONTAINER:-helpdesk-test-pg-pgbackrest}
DBKEY=${FIXTURE_DB_KEY:-pgbackrest-db}
STANZA=main

step() { printf '\n== %s\n' "$*"; }
psql_c() { docker exec "$C" psql -U postgres -d testdb -Atc "$1"; }
auth=(-H "Authorization: Bearer $KEY")

docker inspect -f '{{.State.Running}}' "$C" 2>/dev/null | grep -q true || {
  echo "container $C is not running; start the fixture first (see the header of this script)" >&2
  exit 1
}

if [ "${YES:-}" != 1 ]; then
  echo "This restores $C ($DBKEY) to a point a few seconds in the past. It is destructive for that container only."
  read -r -p "Continue? [y/N] " ans
  [ "$ans" = y ] || exit 1
fi

step "prepare: a full backup, a row before the target, the target, rows after it"
docker exec -u postgres "$C" pgbackrest --stanza=$STANZA --type=full --start-fast --log-level-console=warn backup
psql_c "create table if not exists cycle_marker(phase text, at timestamptz default now())"
CYCLE=$(date -u +%H%M%S)   # tags this cycle's rows; rows of earlier cycles are older than the target and stay
psql_c "insert into cycle_marker(phase) values ('before-$CYCLE')"
sleep 3
TARGET=$(psql_c "select to_char(clock_timestamp() at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
sleep 3
for _ in 1 2 3; do psql_c "insert into cycle_marker(phase) values ('after-$CYCLE')" >/dev/null; done
psql_c "select pg_switch_wal()" >/dev/null
echo "target: $TARGET"
echo "this cycle's rows now: $(psql_c "select phase||'='||count(*) from cycle_marker where phase like '%-$CYCLE' group by phase order by phase" | paste -sd' ' -)"

step "plan (read-only)"
PLAN=$(curl -s -X POST "$SYSADMIN/tool/plan_point_in_time_recovery" \
  -H "Authorization: Bearer $SYSADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"trace_id":"tr_cycle_plan","purpose":"diagnostic","args":{"target":"'"$DBKEY"'","stanza":"'"$STANZA"'","recovery_target_type":"time","recovery_target_value":"'"$TARGET"'"}}' \
  | jq -r .output)
echo "$PLAN" | jq '{status, detail, backup_label, plan_digest,
  discarded: .data_loss_estimate.summary,
  rpo_check: .data_loss_estimate.objective.detail, rpo_gate: .data_loss_estimate.objective.gate,
  budget: .availability_budget.detail, budget_gate: .availability_budget.gate,
  estimate: .availability_budget.recovery_estimate.status, estimate_samples: .availability_budget.recovery_estimate.samples}'
if [ "$(echo "$PLAN" | jq -r '.data_loss_estimate.transactions.status // "absent"')" != measured ]; then
  # The count did not come out. Capture the state now: the first time this happened
  # (a fresh fixture, a plan made seconds after pg_switch_wal) it could not be
  # reproduced afterwards, because the next WAL switch changed both listings.
  echo
  echo "!! transactions were not counted. State at this moment:"
  echo "$PLAN" | jq '.data_loss_estimate.transactions'
  docker exec "$C" sh -c 'ls /var/lib/postgresql/data/pg_wal | head -20'
  docker exec -u postgres "$C" pgbackrest --stanza=$STANZA info | grep "wal archive"
fi
[ "$(echo "$PLAN" | jq -r .status)" = feasible ] || { echo "plan is not feasible; not going on" >&2; exit 1; }
DIGEST=$(echo "$PLAN" | jq -r .plan_digest)
ACK=()
ASKS=no
if [ "$(echo "$PLAN" | jq -r '[.data_loss_estimate.objective.gate, .availability_budget.gate] | index("acknowledge") // empty')" != "" ]; then
  ASKS=yes
fi
case "${ACK_BREACH:-auto}" in
  yes) ACK=(--acknowledge-objective-breach) ;;
  auto) if [ "$ASKS" = yes ]; then
          ACK=(--acknowledge-objective-breach)
          echo "the plan asks for an acknowledgement: approving with --acknowledge-objective-breach"
        fi ;;
  no) echo "ACK_BREACH=no: approving WITHOUT --acknowledge-objective-breach (plan asks for it: $ASKS)" ;;
  *) echo "ACK_BREACH must be auto, yes or no" >&2; exit 1 ;;
esac

step "triage through the gateway (holds at the gate)"
PB=$(curl -s "${auth[@]}" "$GW/api/v1/fleet/playbooks?series_id=pbs_pgbackrest_pitr_triage&active_only=true" | jq -r '.playbooks[0].playbook_id')
RESP=$(curl -s -X POST "$GW/api/v1/fleet/playbooks/$PB/run" "${auth[@]}" -H "Content-Type: application/json" -H "X-Purpose: diagnostic" \
  -d '{"connection_string":"'"$DBKEY"'","gate_escalation":true,"recovery_target":{"type":"time","value":"'"$TARGET"'"}}')
RUN=$(echo "$RESP" | jq -r '.run_id // empty')
[ -n "$RUN" ] || { echo "no run_id in the response:" >&2; echo "$RESP" >&2; exit 1; }
echo "run $RUN: $(echo "$RESP" | jq -r .status)"

step "approve as $APPROVER"
approve() {
  HELPDESK_GATEWAY_URL=$GW HELPDESK_CLIENT_API_KEY=$KEY go run ./cmd/helpdesk-client --user "$APPROVER" \
    --pitr-approve "$RUN" --recovery-target-type time --recovery-target "$TARGET" --plan-digest "$DIGEST" \
    --acknowledge-data-loss --allow-stop-serving "$@"
}
if [ "${ACK_BREACH:-auto}" = no ]; then
  # Show what the second yes does: this approval is expected to be refused (or the
  # restore refused) when the plan asks for an acknowledgement. The gate stays open.
  if approve; then
    echo
    echo "the gateway accepted the approval. When the plan asks for the acknowledgement the restore tool is expected to refuse"
    echo "it (see the incident below); the instance is not stopped. To approve again with the acknowledgement:"
    echo "  HELPDESK_GATEWAY_URL=$GW HELPDESK_CLIENT_API_KEY=... go run ./cmd/helpdesk-client --user $APPROVER --pitr-approve $RUN \\"
    echo "    --recovery-target-type time --recovery-target $TARGET --plan-digest $DIGEST --acknowledge-data-loss --allow-stop-serving --acknowledge-objective-breach"
  else
    echo
    echo "refused, as expected when the plan asks for the acknowledgement. The gate is still open; approve it with:"
    echo "  HELPDESK_GATEWAY_URL=$GW HELPDESK_CLIENT_API_KEY=... go run ./cmd/helpdesk-client --user $APPROVER --pitr-approve $RUN \\"
    echo "    --recovery-target-type time --recovery-target $TARGET --plan-digest $DIGEST --acknowledge-data-loss --allow-stop-serving --acknowledge-objective-breach"
    exit 0
  fi
else
  approve ${ACK[@]+"${ACK[@]}"}
fi

step "wait for the incident to leave open"
STATUS=open
for _ in $(seq 1 120); do
  STATUS=$(curl -s "${auth[@]}" "$GW/api/v1/incidents/$RUN" | jq -r '.incident_record.status // "unknown"')
  [ "$STATUS" != open ] && [ "$STATUS" != unknown ] && break
  sleep 10
done
echo "incident status: $STATUS"

step "verify"
for _ in $(seq 1 30); do docker exec "$C" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 2; done
echo "this cycle's rows after the restore: $(psql_c "select phase||'='||count(*) from cycle_marker where phase like '%-$CYCLE' group by phase order by phase" | paste -sd' ' -)"
echo "expected: before-$CYCLE=1 and no after-$CYCLE rows (rows of earlier cycles are older than the target and stay)"

step "report"
go run ./testing/cmd/faulttest vault incidents "$RUN" --summary --gateway "$GW" --api-key "$KEY" || true
go run ./testing/cmd/faulttest vault budget "$DBKEY" --gateway "$GW" --api-key "$KEY" || true
echo
echo "run id: $RUN"

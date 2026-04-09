#!/usr/bin/env bash
# run-fleet-job.sh — run a fleet job ad-hoc in the cluster.
#
# Creates a temporary ConfigMap + Job, streams the logs, and deletes both on exit.
# The fleet-runner binary inside the cluster container is used; no local binary needed.
#
# Usage:
#   ./scripts/run-fleet-job.sh [OPTIONS] <job-file.json>
#
# Options:
#   -n, --namespace   NS       Kubernetes namespace (default: helpdesk-system)
#   -r, --release     NAME     Helm release name (default: helpdesk)
#   --dry-run                  Pass --dry-run to fleet-runner (plan only, no execution)
#   --api-key         KEY      API key plaintext (sets HELPDESK_CLIENT_API_KEY directly)
#   --api-key-secret  SECRET   Name of K8s Secret with an "api-key" key (alternative to --api-key)
#   --audit-api-key-secret SECRET  K8s Secret for HELPDESK_AUDIT_API_KEY (defaults to --api-key-secret)
#   --canary          N        Override strategy.canary_count
#   --wave-size       N        Override strategy.wave_size
#   --pause           N        Override strategy.wave_pause_seconds
#   -h, --help                 Print this help

set -euo pipefail

# ── helpers ───────────────────────────────────────────────────────────────────

die()  { echo "[fleet-run] ERROR: $*" >&2; exit 1; }
info() { echo "[fleet-run] $*"; }

usage() {
  sed -n '/^# Usage:/,/^[^#]/p' "$0" | sed 's/^# \?//'
  exit 0
}

# ── argument parsing ──────────────────────────────────────────────────────────

NAMESPACE="helpdesk-system"
RELEASE="helpdesk"
JOB_FILE=""
DRY_RUN=false
API_KEY=""
API_KEY_SECRET=""
AUDIT_API_KEY_SECRET=""
CANARY=0
WAVE_SIZE=0
PAUSE_SECS=-1

while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--namespace)   NAMESPACE="$2";    shift 2 ;;
    -r|--release)     RELEASE="$2";      shift 2 ;;
    --dry-run)        DRY_RUN=true;      shift   ;;
    --api-key)               API_KEY="$2";           shift 2 ;;
    --api-key-secret)        API_KEY_SECRET="$2";    shift 2 ;;
    --audit-api-key-secret)  AUDIT_API_KEY_SECRET="$2"; shift 2 ;;
    --canary)         CANARY="$2";       shift 2 ;;
    --wave-size)      WAVE_SIZE="$2";    shift 2 ;;
    --pause)          PAUSE_SECS="$2";   shift 2 ;;
    -h|--help)        usage ;;
    -*)               die "Unknown option: $1" ;;
    *)                JOB_FILE="$1";     shift   ;;
  esac
done

[[ -n "$JOB_FILE" ]]   || die "<job-file.json> is required"
[[ -f "$JOB_FILE"  ]]  || die "Job file not found: $JOB_FILE"
command -v kubectl &>/dev/null || die "kubectl not found in PATH"
command -v jq      &>/dev/null || die "jq not found in PATH"

# ── auto-detect cluster resources ─────────────────────────────────────────────

info "Detecting cluster configuration (namespace=$NAMESPACE, release=$RELEASE)..."

# Image: read from the gateway Deployment (all helpdesk containers share the same image).
IMAGE=$(kubectl -n "$NAMESPACE" get deployment "${RELEASE}-gateway" \
  -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null) \
  || die "Could not find Deployment ${RELEASE}-gateway in namespace $NAMESPACE. " \
         "Are the correct --namespace / --release values set?"
info "Using image: $IMAGE"

GATEWAY_PORT=$(kubectl -n "$NAMESPACE" get svc "${RELEASE}-gateway" \
  -o jsonpath='{.spec.ports[0].port}' 2>/dev/null) || GATEWAY_PORT=8080
AUDITD_PORT=$(kubectl -n "$NAMESPACE" get svc "${RELEASE}-auditd" \
  -o jsonpath='{.spec.ports[0].port}' 2>/dev/null) || AUDITD_PORT=1199
INFRA_CM="${RELEASE}-config"

GATEWAY_URL="http://${RELEASE}-gateway:${GATEWAY_PORT}"
AUDITD_URL="http://${RELEASE}-auditd:${AUDITD_PORT}"

# ── unique names for temporary resources ──────────────────────────────────────

TS=$(date +%s)
JOB_NAME="fleet-adhoc-${TS}"
CM_NAME="fleet-adhoc-job-${TS}"

# ── cleanup trap ──────────────────────────────────────────────────────────────

cleanup() {
  info "Cleaning up..."
  kubectl -n "$NAMESPACE" delete job       "$JOB_NAME" --ignore-not-found=true &>/dev/null || true
  kubectl -n "$NAMESPACE" delete configmap "$CM_NAME"  --ignore-not-found=true &>/dev/null || true
}
trap cleanup EXIT

# ── create the job-definition ConfigMap ───────────────────────────────────────

info "Creating job ConfigMap $CM_NAME..."
kubectl -n "$NAMESPACE" create configmap "$CM_NAME" \
  --from-file=job.json="$JOB_FILE"

# ── build fleet-runner command ────────────────────────────────────────────────

FLEET_CMD=(
  /usr/local/bin/fleet-runner
  "--job-file=/etc/helpdesk/fleet-jobs/job.json"
  "--gateway=${GATEWAY_URL}"
  "--audit-url=${AUDITD_URL}"
  "--infra=/etc/helpdesk/infrastructure.json"
)
$DRY_RUN           && FLEET_CMD+=(--dry-run)
[[ $CANARY    -gt 0 ]] && FLEET_CMD+=(--canary="$CANARY")
[[ $WAVE_SIZE -gt 0 ]] && FLEET_CMD+=(--wave-size="$WAVE_SIZE")
[[ $PAUSE_SECS -ge 0 ]] && FLEET_CMD+=(--pause="$PAUSE_SECS")

# ── build env block (JSON) ────────────────────────────────────────────────────

ENV_JSON='[{"name":"HELPDESK_SESSION_PURPOSE","value":"fleet_rollout"}]'

if [[ -n "$API_KEY" ]]; then
  ENV_JSON=$(jq -c '. + [{"name":"HELPDESK_CLIENT_API_KEY","value":"'"$API_KEY"'"}]' <<<"$ENV_JSON")
  ENV_JSON=$(jq -c '. + [{"name":"HELPDESK_AUDIT_API_KEY","value":"'"$API_KEY"'"}]' <<<"$ENV_JSON")
elif [[ -n "$API_KEY_SECRET" ]]; then
  ENV_JSON=$(jq -c '. + [{"name":"HELPDESK_CLIENT_API_KEY","valueFrom":{"secretKeyRef":{"name":"'"$API_KEY_SECRET"'","key":"api-key"}}}]' <<<"$ENV_JSON")
  # Default audit key to the same secret unless a dedicated one was supplied.
  AUDIT_SECRET="${AUDIT_API_KEY_SECRET:-$API_KEY_SECRET}"
  ENV_JSON=$(jq -c '. + [{"name":"HELPDESK_AUDIT_API_KEY","valueFrom":{"secretKeyRef":{"name":"'"$AUDIT_SECRET"'","key":"api-key"}}}]' <<<"$ENV_JSON")
elif [[ -n "$AUDIT_API_KEY_SECRET" ]]; then
  ENV_JSON=$(jq -c '. + [{"name":"HELPDESK_AUDIT_API_KEY","valueFrom":{"secretKeyRef":{"name":"'"$AUDIT_API_KEY_SECRET"'","key":"api-key"}}}]' <<<"$ENV_JSON")
fi

# Convert FLEET_CMD array to JSON array.
CMD_JSON="["
for elem in "${FLEET_CMD[@]}"; do
  CMD_JSON+=$(jq -cn --arg v "$elem" '$v')","
done
CMD_JSON="${CMD_JSON%,}]"

# ── create the Job ────────────────────────────────────────────────────────────

info "Creating Job $JOB_NAME..."
kubectl -n "$NAMESPACE" apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: ${JOB_NAME}
  labels:
    app.kubernetes.io/managed-by: run-fleet-job.sh
    app.kubernetes.io/component: fleet-runner
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/component: fleet-runner
    spec:
      restartPolicy: Never
      containers:
        - name: fleet-runner
          image: "${IMAGE}"
          imagePullPolicy: IfNotPresent
          command: $(echo "$CMD_JSON")
          env: $(echo "$ENV_JSON")
          volumeMounts:
            - name: infra-config
              mountPath: /etc/helpdesk/infrastructure.json
              subPath: infrastructure.json
              readOnly: true
            - name: job-definition
              mountPath: /etc/helpdesk/fleet-jobs
              readOnly: true
      volumes:
        - name: infra-config
          configMap:
            name: ${INFRA_CM}
        - name: job-definition
          configMap:
            name: ${CM_NAME}
EOF

# ── wait for pod to start and stream logs ─────────────────────────────────────

info "Waiting for pod to start..."
for i in $(seq 1 30); do
  POD=$(kubectl -n "$NAMESPACE" get pods -l "job-name=${JOB_NAME}" \
    --field-selector 'status.phase!=Pending' \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null) && \
    [[ -n "$POD" ]] && break
  sleep 2
done

if [[ -z "${POD:-}" ]]; then
  # Still in Pending — wait a bit more, then try to log anyway.
  kubectl -n "$NAMESPACE" wait --for=condition=Ready \
    pod -l "job-name=${JOB_NAME}" --timeout=120s &>/dev/null || true
  POD=$(kubectl -n "$NAMESPACE" get pods -l "job-name=${JOB_NAME}" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null) || true
fi

if [[ -z "${POD:-}" ]]; then
  die "No pod found for job $JOB_NAME — check: kubectl -n $NAMESPACE describe job $JOB_NAME"
fi

info "Streaming logs from pod $POD..."
echo "────────────────────────────────────────────────────────────────────────────"
kubectl -n "$NAMESPACE" logs -f "$POD" || true
echo "────────────────────────────────────────────────────────────────────────────"

# ── report exit status ────────────────────────────────────────────────────────

# Check the Job condition — more reliable than reading the pod's terminated exit
# code, which may not be written yet when kubectl logs -f returns.
kubectl -n "$NAMESPACE" wait job/"$JOB_NAME" \
  --for=condition=Complete --timeout=10s &>/dev/null && JOB_OK=true || JOB_OK=false

# ── print per-step output from auditd ─────────────────────────────────────────
# The fleet-runner logs only show orchestration events; actual tool output is
# stored in auditd's step records. Fetch and print them if we have a job ID.
#
# JOB_ID is parsed from the fleet-runner logs (line: "fleet job created job_id=...").
JOB_ID=$(kubectl -n "$NAMESPACE" logs "$POD" 2>/dev/null \
  | grep 'fleet job created' | grep -o 'job_id=[^ ]*' | cut -d= -f2)

# Resolve the API key for curl auth against auditd (uses audit key if available).
CURL_API_KEY=""
if [[ -n "$API_KEY" ]]; then
  CURL_API_KEY="$API_KEY"
elif [[ -n "$AUDIT_API_KEY_SECRET" ]]; then
  CURL_API_KEY=$(kubectl -n "$NAMESPACE" get secret "$AUDIT_API_KEY_SECRET" \
    -o jsonpath='{.data.api-key}' 2>/dev/null | base64 -d 2>/dev/null) || CURL_API_KEY=""
elif [[ -n "$API_KEY_SECRET" ]]; then
  CURL_API_KEY=$(kubectl -n "$NAMESPACE" get secret "$API_KEY_SECRET" \
    -o jsonpath='{.data.api-key}' 2>/dev/null | base64 -d 2>/dev/null) || CURL_API_KEY=""
fi
CURL_AUTH=()
[[ -n "$CURL_API_KEY" ]] && CURL_AUTH=(-H "Authorization: Bearer ${CURL_API_KEY}")

if [[ -n "$JOB_ID" ]]; then
  # Port-forward auditd on a random local port, fetch results, then kill it.
  AUDITD_SVC="${RELEASE}-auditd"
  AUDITD_PORT=$(kubectl -n "$NAMESPACE" get svc "$AUDITD_SVC" \
    -o jsonpath='{.spec.ports[0].port}' 2>/dev/null) || AUDITD_PORT=1199
  LOCAL_PORT=$(( RANDOM % 10000 + 40000 ))

  kubectl -n "$NAMESPACE" port-forward \
    "svc/${AUDITD_SVC}" "${LOCAL_PORT}:${AUDITD_PORT}" &>/dev/null &
  PF_PID=$!
  sleep 1  # give port-forward a moment to connect

  SERVERS=$(curl -sf "${CURL_AUTH[@]}" \
    "http://localhost:${LOCAL_PORT}/v1/fleet/jobs/${JOB_ID}/servers" 2>/dev/null \
    | jq -r '.[].server_name' 2>/dev/null) || SERVERS=""

  if [[ -n "$SERVERS" ]]; then
    echo ""
    echo "════════════════════════════════════════════════════════════════════════════"
    echo "  Fleet job results: $JOB_ID"
    echo "════════════════════════════════════════════════════════════════════════════"

    # Collect all steps into a single JSON array: [{server, steps:[...]}, ...]
    ALL_RESULTS="[]"
    while IFS= read -r server; do
      STEPS=$(curl -sf "${CURL_AUTH[@]}" \
        "http://localhost:${LOCAL_PORT}/v1/fleet/jobs/${JOB_ID}/servers/${server}/steps" \
        2>/dev/null) || STEPS="[]"
      ALL_RESULTS=$(echo "$ALL_RESULTS" \
        | jq --arg s "$server" --argjson st "$STEPS" '. + [{server:$s, steps:$st}]')
    done <<<"$SERVERS"

    # If any step is get_status_summary, render as a table; otherwise per-step text.
    HAS_SUMMARY=$(echo "$ALL_RESULTS" \
      | jq '[.[].steps[] | select(.tool=="get_status_summary")] | length > 0')

    if [[ "$HAS_SUMMARY" == "true" ]]; then
      echo ""
      echo "$ALL_RESULTS" | jq -r '
        ["SERVER", "STATUS", "VERSION", "UPTIME", "CONN", "CACHE HIT%"],
        ["──────", "──────", "───────", "──────", "────", "──────────"],
        (.[] |
          .server as $srv |
          .steps[] |
          select(.tool == "get_status_summary") |
          .output as $out |
          ($out | try fromjson catch {status:"error",version:"-",uptime:"-",connections:0,max_connections:0,cache_hit_ratio:0}) as $s |
          [$srv,
           $s.status,
           ($s.version // "-"),
           ($s.uptime  // "-"),
           (($s.connections // 0 | tostring) + "/" + ($s.max_connections // 0 | tostring)),
           ($s.cache_hit_ratio // 0 | tostring)]
        ) | @tsv' | column -t -s $'\t'
      echo ""
    else
      while IFS= read -r server; do
        echo ""
        echo "  Server: $server"
        echo "  ──────────────────────────────────────────────────────────────────────"
        STEPS=$(echo "$ALL_RESULTS" \
          | jq -r --arg s "$server" '.[] | select(.server==$s) | .steps')
        echo "$STEPS" | jq -r '.[] | "  [step \(.step_index)] \(.tool)", (.output // "  (no output)")'
      done <<<"$SERVERS"
      echo ""
    fi
  fi

  kill "$PF_PID" 2>/dev/null; wait "$PF_PID" 2>/dev/null || true
fi

if $JOB_OK; then
  info "Fleet job completed successfully."
  exit 0
else
  EXIT_CODE=$(kubectl -n "$NAMESPACE" get pod "$POD" \
    -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null)
  info "Fleet job FAILED (exit code ${EXIT_CODE:-unknown})."
  exit 1
fi

#!/bin/bash
# Takes the initial full backup for the pgBackRest test target, run as an
# explicit step AFTER the container reports healthy — see
# init-pgbackrest.sh's own comment for why this can't reliably happen during
# container init (races against the temp server's own checkpoint/shutdown
# timing; confirmed live, 2026-09-26).
set -euo pipefail
docker exec -u postgres helpdesk-test-pg-pgbackrest \
    pgbackrest --stanza=main --type=full --log-level-console=info backup

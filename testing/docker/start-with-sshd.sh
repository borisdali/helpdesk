#!/bin/bash
# Starts sshd in the background, then execs the base postgres image's own
# entrypoint with whatever args docker-compose's `command:` supplies —
# preserves normal postgres startup behavior unchanged, sshd is purely
# additive. Exists so the sysadmin agent's SSH-dispatch path
# (agents/sysadmin/sshexec.go) has a real, separate connection path to test
# pgBackRest's own SSH dispatch against, distinct from the docker-exec path
# already covered by db-pgbackrest-repo-unreadable.
set -euo pipefail

/usr/sbin/sshd

exec /usr/local/bin/docker-entrypoint.sh "$@"

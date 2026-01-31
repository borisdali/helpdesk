#!/bin/bash
# Kill all flood psql processes spawned by inject_max_connections.sh.
if [ -f /tmp/flood_pids.txt ]; then
    while read pid; do
        kill "$pid" 2>/dev/null
    done < /tmp/flood_pids.txt
    rm -f /tmp/flood_pids.txt
    echo "Killed flood connections"
else
    # Fallback: kill any pg_sleep sessions.
    pkill -f "pg_sleep(3600)" 2>/dev/null
    echo "No PID file found, attempted pkill fallback"
fi

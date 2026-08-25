#!/usr/bin/env bash
# Build and run OpenMOS (MOS 2.8.4 TCP receive tracer) for the live NCS interop
# exercise, bound to loopback only, and confirm it is listening.
#
# MOS 2.x over TCP has no authentication. Bind loopback only and reach it
# exclusively through the SSH reverse tunnel (see open-tunnel.sh).
set -u

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT/src" || exit 1
mkdir -p "$REPO_ROOT/tmp"

PIDFILE="$REPO_ROOT/tmp/openmos.pid"
LOGFILE="$REPO_ROOT/tmp/openmos.log"

# Stop any previous instance from an earlier run.
if [ -f "$PIDFILE" ]; then
  OLD=$(cat "$PIDFILE")
  if kill -0 "$OLD" 2>/dev/null; then
    echo "stopping previous openmos pid $OLD"
    kill "$OLD" 2>/dev/null
    sleep 1
  fi
fi

go build -o openmos . || { echo "BUILD FAILED"; exit 1; }

nohup ./openmos --config=config.yaml </dev/null > "$LOGFILE" 2>&1 &
PID=$!
disown "$PID" 2>/dev/null
echo "$PID" > "$PIDFILE"
echo "started openmos pid $PID"

for _ in $(seq 1 20); do
  if lsof -nP -iTCP:10541 -sTCP:LISTEN 2>/dev/null | grep -q openmos; then
    echo "LISTENING on 10541"
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "PROCESS DIED"
    break
  fi
  sleep 0.5
done

echo "--- listener ---"
lsof -nP -iTCP:10541 -sTCP:LISTEN 2>/dev/null || echo "no listener"
echo "--- log ---"
tail -25 "$LOGFILE"

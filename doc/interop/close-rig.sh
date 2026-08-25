#!/usr/bin/env bash
# Tear down the interop rig: close the SSH reverse tunnel and stop OpenMOS.
#
# Optional:
#   NCS_SSH_HOST   ssh alias for the ENPS/NOM host (needed to close the tunnel
#                  cleanly; if unset the control socket is simply removed)
set -u

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CTL="$REPO_ROOT/tmp/mos-tunnel.ctl"
PIDFILE="$REPO_ROOT/tmp/openmos.pid"

echo "=== closing tunnel ==="
if [ -S "$CTL" ]; then
  if [ -n "${NCS_SSH_HOST:-}" ]; then
    ssh -S "$CTL" -O exit "$NCS_SSH_HOST" </dev/null 2>&1 | tail -1
  else
    echo "NCS_SSH_HOST unset; removing control socket without a clean exit"
  fi
  rm -f "$CTL"
else
  echo "no tunnel control socket"
fi

echo "=== stopping openmos ==="
if [ -f "$PIDFILE" ]; then
  PID=$(cat "$PIDFILE")
  if kill -0 "$PID" 2>/dev/null; then
    kill "$PID" && echo "sent TERM to $PID"
  else
    echo "pid $PID not running"
  fi
  rm -f "$PIDFILE"
else
  echo "no pidfile"
fi

sleep 2
echo "=== verify ==="
if lsof -nP -iTCP:10541 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "WARNING: something is still listening on 10541"
else
  echo "openmos: DOWN"
fi
[ -S "$CTL" ] && echo "WARNING: tunnel socket still present" || echo "tunnel: DOWN"
echo
echo "Confirm from the NCS side that the forwarded port is gone, e.g.:"
echo "  Test-NetConnection -ComputerName 127.0.0.1 -Port \${REMOTE_PORT:-20541} -InformationLevel Quiet"

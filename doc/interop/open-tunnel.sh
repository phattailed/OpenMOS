#!/usr/bin/env bash
# Establish an SSH reverse tunnel so the NCS host can reach OpenMOS running on
# this workstation.
#
#   NCS 127.0.0.1:$REMOTE_PORT  ->  (ssh -R)  ->  workstation 127.0.0.1:$LOCAL_PORT
#
# The remote port defaults to 20541 because NOM normally already owns 10540/10541
# on the NCS host. Binding the remote loopback only (no GatewayPorts) keeps the
# unauthenticated MOS port off the NCS network.
#
# Required:
#   NCS_SSH_HOST   ssh alias or user@host for the ENPS/NOM machine
# Optional:
#   NCS_SSH_JUMP   ssh jump host, if the NCS is not directly reachable
#   REMOTE_PORT    default 20541
#   LOCAL_PORT     default 10541
#
# NOTE: ssh -f daemonizes while inheriting stdout/stderr. If those are left
# attached to a pipe, the caller's pipe never sees EOF and the command appears to
# hang. All ssh output is therefore redirected to a log file.
set -u

: "${NCS_SSH_HOST:?set NCS_SSH_HOST to the ssh alias for the ENPS/NOM host}"
REMOTE_PORT="${REMOTE_PORT:-20541}"
LOCAL_PORT="${LOCAL_PORT:-10541}"

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$REPO_ROOT/tmp"
CTL="$REPO_ROOT/tmp/mos-tunnel.ctl"
LOG="$REPO_ROOT/tmp/tunnel-ssh.log"

JUMP_ARGS=()
if [ -n "${NCS_SSH_JUMP:-}" ]; then
  JUMP_ARGS=(-J "$NCS_SSH_JUMP")
fi

if [ -S "$CTL" ]; then
  echo "closing previous tunnel"
  ssh -S "$CTL" -O exit "$NCS_SSH_HOST" >>"$LOG" 2>&1
  sleep 1
fi
rm -f "$CTL"

ssh -M -S "$CTL" -fNT \
    -o ExitOnForwardFailure=yes \
    -o ServerAliveInterval=30 \
    -o ServerAliveCountMax=3 \
    -o BatchMode=yes \
    -o ConnectTimeout=25 \
    "${JUMP_ARGS[@]}" \
    -R "${REMOTE_PORT}:127.0.0.1:${LOCAL_PORT}" \
    "$NCS_SSH_HOST" </dev/null >>"$LOG" 2>&1
RC=$?

if [ $RC -ne 0 ]; then
  echo "TUNNEL FAILED rc=$RC"
  tail -5 "$LOG"
  exit $RC
fi

echo "tunnel established: NCS:${REMOTE_PORT} -> localhost:${LOCAL_PORT}"
ssh -S "$CTL" -O check "$NCS_SSH_HOST" </dev/null 2>&1 | tail -1
echo "teardown: bash doc/interop/close-rig.sh"
exit 0

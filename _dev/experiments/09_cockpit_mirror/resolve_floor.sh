#!/bin/bash
# Experiment 09d: the FLOOR of today's live `master-current` resolve, split into CLI
# process overhead vs the daemon's routed round trip (READ-ONLY against the live
# local daemon — no writes, no restarts). Compare with the mesh RTT.
SOCK=$HOME/.sesh/daemon.sock
BIN=$HOME/.local/bin/sesh
ms() { echo $(( ($2 - $1) / 1000000 )); }
PEER=${1:-macbook}
for i in 1 2 3 4; do
  a=$(date +%s%N)
  SESH_HOME=$HOME/.sesh "$BIN" tmux master-current --origin "$PEER" --json >/dev/null 2>&1
  b=$(date +%s%N)
  SESH_HOME=$HOME/.sesh "$BIN" tmux master-current --origin "$(hostname)" --machine "$PEER" --json >/dev/null 2>&1
  c=$(date +%s%N)
  curl -s --unix-socket "$SOCK" "http://x/v1/tmux/master-current?origin=mymain&machine=$PEER" >/dev/null
  d=$(date +%s%N)
  curl -s --unix-socket "$SOCK" "http://x/v1/tmux/master-current?origin=$PEER" >/dev/null
  e=$(date +%s%N)
  echo "CLI local $(ms $a $b) | CLI routed->$PEER $(ms $b $c) | raw-unix routed $(ms $c $d) | raw-unix local $(ms $d $e) ms"
done
ping -c 3 -q "$PEER" | tail -1

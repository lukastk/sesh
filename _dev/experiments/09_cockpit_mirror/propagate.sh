#!/bin/bash
# Experiment 09c: END-TO-END propagation latency of a work-server client change
# from machine B to machine A's cached mesh view, over the REAL http mesh path.
#
# Stand-in: a thread's `attachment` is computed from the SAME per-tick list-clients
# (maintainer, 300ms) that a cockpit mirror would read, and replicated by the SAME
# mesh sync/delta path. So its latency IS the mirror's latency.
#
# Two real daemons (isolated homes/sockets/ports, never the live fleet), A registers
# B as an http peer. Measures (1) under DEMAND (A's mesh read continuously), and
# (2) after an IDLE stretch: what a single read right after the move sees, and how
# long the demand kick takes to make it fresh.
set -u
BIN=$(dirname "$0")/sesh
IDLE=${IDLE:-20s}
R=$RANDOM; PA=$((17000 + R % 1000)); PB=$((18000 + R % 1000)); TOK=tok$R
HA=$(mktemp -d /tmp/i13a.XXXX); HB=$(mktemp -d /tmp/i13b.XXXX)
printf '[mesh]\nidle_interval = "%s"\n' "$IDLE" > "$HA/config.toml"
printf '[mesh]\nidle_interval = "%s"\n' "$IDLE" > "$HB/config.toml"
envA=(SESH_HOME=$HA SESH_MACHINE=pA SESH_TMUX_SOCKET=i13wA$R SESH_MASTER_SOCKET=i13mA$R SESH_API_ADDR=127.0.0.1:$PA SESH_API_TOKEN=$TOK)
envB=(SESH_HOME=$HB SESH_MACHINE=pB SESH_TMUX_SOCKET=i13wB$R SESH_MASTER_SOCKET=i13mB$R SESH_API_ADDR=127.0.0.1:$PB SESH_API_TOKEN=$TOK)
cleanup() {
  env "${envA[@]}" "$BIN" daemon stop >/dev/null 2>&1; env "${envB[@]}" "$BIN" daemon stop >/dev/null 2>&1
  tmux -L i13wB$R kill-server 2>/dev/null; tmux -L i13wA$R kill-server 2>/dev/null
  rm -rf "$HA" "$HB"
}
trap cleanup EXIT
env "${envB[@]}" "$BIN" daemon run > "$HB/d.log" 2>&1 &
env "${envA[@]}" "$BIN" daemon run > "$HA/d.log" 2>&1 &
sleep 1.5
env "${envA[@]}" "$BIN" peer add --machine pB --ssh localhost --home "$HB" --tmux-socket i13wB$R --api-addr 127.0.0.1:$PB --api-token $TOK >/dev/null
TID=$(env "${envB[@]}" "$BIN" shell new --cwd /tmp --name mirror-probe --json | jq -r '.thread.id // .id')
SESS=$(env "${envB[@]}" "$BIN" shell info --id "$TID" --json 2>/dev/null | jq -r '.session_name // .thread.session_name // empty')
[ -n "$SESS" ] || SESS=$(tmux -L i13wB$R list-sessions -F '#{session_name}' | head -1)
echo "thread=$TID session=$SESS"
now_ms() { date +%s%3N; }
att_on_A() { env "${envA[@]}" "$BIN" mesh --json | jq -r --arg id "$TID" '.machines[] | select(.machine=="pB") | .threads[] | select(.id==$id) | .attachment'; }
wait_for() { # $1 = wanted attachment; prints ms until A's cached view shows it
  local t0=$2
  while [ "$(att_on_A)" != "$1" ]; do sleep 0.02; [ $(( $(now_ms) - t0 )) -gt 90000 ] && { echo TIMEOUT; return; }; done
  echo $(( $(now_ms) - t0 ))
}
# baseline: detached, visible on A
echo "baseline on A: $(att_on_A) (waiting until A has the row...)"; t=$(now_ms); wait_for detached "$t" >/dev/null
echo "--- DEMAND (A reading continuously => 1Hz rounds) ---"
for i in 1 2 3 4 5; do
  TERM=xterm-256color script -q -c "tmux -L i13wB$R attach -t '=$SESS'" /dev/null >/dev/null 2>&1 &
  CP=$!; t=$(now_ms); echo "attach  -> visible on A after $(wait_for attached "$t") ms"
  sleep 0.$((RANDOM % 9)); tmux -L i13wB$R detach-client -a -s "=$SESS" 2>/dev/null || tmux -L i13wB$R detach-client -s "=$SESS"
  t=$(now_ms); echo "detach  -> visible on A after $(wait_for detached "$t") ms"; wait $CP 2>/dev/null
  sleep 0.$((RANDOM % 9))
done
echo "--- IDLE (no A reads for 65s => past the 60s active window; idle_interval=$IDLE) ---"
sleep 65
TERM=xterm-256color script -q -c "tmux -L i13wB$R attach -t '=$SESS'" /dev/null >/dev/null 2>&1 &
sleep 0.5; t=$(now_ms)
first=$(att_on_A); echo "move 0.5s ago; FIRST read after idle sees: $first  (a press here would use this)"
echo "time until the kicked round makes it fresh: $(wait_for attached "$t") ms after that read"
echo "A's reported cadence: $(env "${envA[@]}" "$BIN" daemon status --json 2>/dev/null | jq -r '.mesh_cadence // empty')"

#!/bin/bash
# Experiment 09a: which work-server hooks fire, and does a POLLED per-client
# list-clients report the marker client correctly, for every way its view changes?
# Isolated server; a MARKER client M and a DECOY client D (H98 discipline: D moves
# last before each read, so an ambient resolve would report D's view).
set -u
S=i13h$$; LOG=$(mktemp); T="tmux -L $S"
$T -f /dev/null new-session -d -s A -x 120 -y 30 'sleep 999'
$T new-window -d -t A: 'sleep 999'
$T new-session -d -s B -x 120 -y 30 'sleep 999'; $T new-window -d -t B: 'sleep 999'; $T split-window -d -t B:1 'sleep 999'
$T new-session -d -s C -x 120 -y 30 'sleep 999'
# stamp @sesh-thread-id on every pane = "<session>.<window>.<pane>"
for p in $($T list-panes -a -F '#{pane_id}'); do
  $T set-option -p -t "$p" @sesh-thread-id "$($T display -p -t "$p" '#{session_name}.#{window_index}.#{pane_index}')"
done
for h in client-session-changed session-window-changed window-pane-changed client-detached client-attached session-closed after-select-window after-select-pane after-switch-client after-new-window pane-focus-in client-active; do
  $T set-hook -g "$h" "run-shell -b 'echo $h >> $LOG'"
done
$T set -g focus-events on
TERM=xterm-256color script -q -c "tmux -L $S attach -t A" /dev/null >/dev/null 2>&1 &
sleep 1.5; M=$($T list-clients -F '#{client_name}' | head -1)
TERM=xterm-256color script -q -c "tmux -L $S attach -t C" /dev/null >/dev/null 2>&1 &
sleep 1.5; D=$($T list-clients -F '#{client_name}' | grep -vx "$M" | head -1)
MP=$($T list-clients -F '#{client_name} #{client_pid}' | grep "^$M ")
echo "tmux $($T -V | cut -d' ' -f2)  marker=[$MP] decoy=[$D]"
read_m() {
  $T list-clients -F '#{client_name} #{client_pid}|#{@sesh-thread-id}|#{client_session}|#{window_index}' |
    awk -v m="$MP" -F'|' '$1==m{print $2" sess="$3" win="$4; f=1} END{if(!f)print "(marker client gone)"}'
}
m_pane() { $T list-clients -F '#{client_name} #{pane_id}' | awk -v m="$M" '$1==m{print $2}'; }
m_sess() { $T list-clients -F '#{client_name} #{client_session}' | awk -v m="$M" '$1==m{print $2}'; }
step() { # $1 label; rest = command
  local label=$1; shift; : > "$LOG"; "$@"; sleep 0.4
  $T switch-client -c "$D" -t C 2>/dev/null   # decoy moves LAST
  sleep 0.3
  printf '%-40s -> %-26s hooks: %s\n' "$label" "$(read_m)" "$(sort -u "$LOG" | tr '\n' ' ')"
}
pick_via_choose_tree() {
  # a real interactive picker on M's pane: open choose-tree, move down, Enter
  $T choose-tree -Z -t "$(m_pane)" ; sleep 0.3
  $T send-keys -t "$(m_pane)" Down Down Enter
}
kill_under_m() { $T kill-session -t "=$(m_sess)"; }
step "baseline (M on A)"                         true
step "switch-client (sesh nav) -> B"             $T switch-client -c "$M" -t B
step "select-window B:1 (M's session)"           $T select-window -t B:1
step "select-pane in B:1"                        $T select-pane -t B:1.1
step "new-window in B (becomes active)"          $T new-window -t B: 'sleep 999'
step "kill-window (active) in B"                 $T kill-window -t B:2
step "choose-tree pick (a base picker)"          pick_via_choose_tree
step "switch-client -n (next session)"           $T switch-client -c "$M" -n
step "kill-session under M (detach-on-destroy)"  kill_under_m
$T kill-server; rm -f "$LOG"

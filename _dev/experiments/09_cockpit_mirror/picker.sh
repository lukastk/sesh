#!/bin/bash
# Experiment 09b: a base-level PICKER (choose-tree) that really changes the marker
# client's session — does the polled per-client list-clients see it?
S=i13c$$; T="tmux -L $S"
$T -f /dev/null new-session -d -s A 'sleep 99'; $T new-session -d -s B 'sleep 99'
$T set-option -p -t A:0.0 @sesh-thread-id THREAD-A; $T set-option -p -t B:0.0 @sesh-thread-id THREAD-B
TERM=xterm-256color script -q -c "tmux -L $S attach -t B" /dev/null >/dev/null 2>&1 &
sleep 1.5
P=$($T list-clients -F '#{pane_id}')
echo "before:                  $($T list-clients -F '#{@sesh-thread-id} #{client_session}')"
$T choose-tree -s -Z -t "$P"; sleep 0.4; $T send-keys -t "$P" 0; sleep 0.4
echo "after choose-tree key 0: $($T list-clients -F '#{@sesh-thread-id} #{client_session}')"
$T kill-server

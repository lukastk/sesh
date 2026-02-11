#!/usr/bin/env bash
# utils.sh — Shell utilities for sesh
# Source this file in your shell config:
#   source /path/to/sesh/utils.sh

# enter-sesh: switch to a sesh's tmux session (or cd if no tmux).
# All arguments are forwarded to `sesh switch`.
#   enter-sesh              # interactive picker (fzf or --tree)
#   enter-sesh myproject    # direct switch
#   enter-sesh --tree       # tree picker
function enter-sesh() {
    local result
    result=$(sesh switch "$@")
    if [ -z "$result" ]; then return 1; fi

    local tmux_session dir
    tmux_session=$(echo "$result" | jq -r '.tmux_session // empty')
    dir=$(echo "$result" | jq -r '.dir')

    if [ -n "$tmux_session" ]; then
        if [ -n "$TMUX" ]; then
            tmux switch-client -t "$tmux_session"
        else
            tmux attach-session -t "$tmux_session"
        fi
    else
        cd "$dir" || return 1
    fi
}

# sesh-new: create a new session with tmux and enter it.
# All arguments are forwarded to `sesh new --tmux`.
#   sesh-new myproject --dir ~/code/myproject
#   sesh-new --parent myproject
function sesh-new() {
    local stderr
    stderr=$(sesh new --tmux "$@" 2>&1 1>/dev/null)
    local rc=$?
    if [ $rc -ne 0 ]; then
        echo "$stderr" >&2
        return $rc
    fi
    echo "$stderr" >&2

    # Parse session name from "Created session '<name>' → ..."
    local name
    name=$(echo "$stderr" | sed -n "s/^Created session '\\(.*\\)'.*/\\1/p")
    if [ -z "$name" ]; then
        echo "sesh-new: could not determine session name" >&2
        return 1
    fi

    enter-sesh "$name"
}

# sesh-prompt: output the current sesh name for use in $PS1 / $PROMPT.
# Prints the session name when inside a sesh, or "~" when outside.
#   PS1='$(sesh-prompt) $ '        # bash
#   PROMPT='$(sesh-prompt) %# '    # zsh
function sesh-prompt() {
    local result
    result=$(sesh info --json 2>/dev/null)
    if [ $? -eq 0 ] && [ -n "$result" ]; then
        echo "$result" | jq -r '.name'
    else
        echo "~"
    fi
}

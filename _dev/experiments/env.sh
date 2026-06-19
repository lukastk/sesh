# Shared ISOLATED sesh env for UI experiments. Source me: `source _dev/experiments/env.sh`
# Never touches the live daemon — own home, own tmux sockets, own API port.
export SESH_BIN=/tmp/sesh-ui-exp
export SESH_HOME=/tmp/sesh-ui-exp-home
export SESH_MACHINE=local
export SESH_TMUX_SOCKET=sesh-ui-exp
export SESH_MASTER_SOCKET=sesh-ui-exp-master
export SESH_CODEX_HOME=/tmp/sesh-ui-exp-codex
export SESH_API_ADDR=127.0.0.1:8979
export SESH_API_TOKEN=uiexptoken123
mkdir -p "$SESH_HOME"
# Isolated codex home needs auth (mirrors the conformance harness's setupCodexHome) or
# codex turns fail with "CODEX_HOME … does not exist". Symlink the real auth in.
mkdir -p "$SESH_CODEX_HOME"
[ -f "$HOME/.codex/auth.json" ] && ln -sf "$HOME/.codex/auth.json" "$SESH_CODEX_HOME/auth.json"
alias seshx="$SESH_BIN"

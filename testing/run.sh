#!/usr/bin/env bash
# testing/run.sh — Demo script exercising sesh features with an isolated data directory.
# Usage: bash testing/run.sh   (from the repo root)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="$SCRIPT_DIR/data"
CONFIG="$SCRIPT_DIR/config.json"
GLOBAL="--data-dir $DATA_DIR --config $CONFIG"

# Write a dummy config so load_config finds it
echo '{}' > "$CONFIG"
mkdir -p "$SCRIPT_DIR/workdirs"

sesh() { uv run --project "$REPO_ROOT" sesh "$@"; }

header() {
  echo ""
  echo "=== $1 ==="
}

# --- Create session hierarchy ---
# webapp (root)
#   ├── webapp-api
#   └── webapp-frontend
#         └── webapp-frontend-v2

header "Creating sessions"
sesh $GLOBAL new webapp        --dir "$SCRIPT_DIR/workdirs/webapp"
sesh $GLOBAL new webapp-api    --dir "$SCRIPT_DIR/workdirs/webapp-api"    --parent webapp --tag backend --tag api
sesh $GLOBAL new webapp-frontend --dir "$SCRIPT_DIR/workdirs/webapp-frontend" --parent webapp --tag frontend
sesh $GLOBAL new webapp-frontend-v2 --dir "$SCRIPT_DIR/workdirs/webapp-frontend-v2" --parent webapp-frontend --tag frontend --tag v2

header "List (table)"
sesh $GLOBAL list

header "List (tree)"
sesh $GLOBAL list --tree

header "List (JSON)"
sesh $GLOBAL list --json | python3 -m json.tool

header "Info for webapp-frontend"
sesh $GLOBAL info webapp-frontend

header "Info for webapp-frontend (JSON)"
sesh $GLOBAL info webapp-frontend --json | python3 -m json.tool

# --- Archive and restore ---
header "Archive webapp-api"
sesh $GLOBAL archive webapp-api

header "List after archive"
sesh $GLOBAL list

header "List --all (includes archived)"
sesh $GLOBAL list --all

header "Restore webapp-api"
sesh $GLOBAL restore webapp-api

header "List after restore"
sesh $GLOBAL list

# --- Delete ---
header "Delete webapp-frontend-v2"
sesh $GLOBAL delete webapp-frontend-v2 --force

header "Tree after delete"
sesh $GLOBAL list --tree

echo ""
echo "All tests passed."

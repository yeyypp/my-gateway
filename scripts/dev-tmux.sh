#!/usr/bin/env bash

set -e

SESSION="ai-gateway-lab"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
: "${LOCAL_CLIENT_KEY_A:?LOCAL_CLIENT_KEY_A must be set by make dev}"
: "${LOCAL_CLIENT_KEY_B:?LOCAL_CLIENT_KEY_B must be set by make dev}"

if tmux has-session -t "$SESSION" 2>/dev/null; then
  echo "tmux session already exists: $SESSION"
else
  tmux new-session -d -s "$SESSION" -n dev -c "$PROJECT_DIR" \
    -e "LOCAL_CLIENT_KEY_A=$LOCAL_CLIENT_KEY_A" \
    -e "LOCAL_CLIENT_KEY_B=$LOCAL_CLIENT_KEY_B"

  gateway_pane="$(tmux display-message -p -t "$SESSION:dev.0" '#{pane_id}')"
  command_pane="$(tmux split-window -h -p 50 -P -F '#{pane_id}' -t "$gateway_pane" -c "$PROJECT_DIR")"

  tmux send-keys -t "$gateway_pane" 'make run-gateway' C-m
  tmux select-pane -t "$command_pane"
fi

if [ -n "${TMUX:-}" ]; then
  tmux switch-client -t "$SESSION"
else
  tmux attach-session -t "$SESSION"
fi

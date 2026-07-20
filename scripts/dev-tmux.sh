#!/usr/bin/env bash

set -e

SESSION="gateway-lab"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

if tmux has-session -t "$SESSION" 2>/dev/null; then
  echo "tmux session already exists: $SESSION"
else
  tmux new-session -d -s "$SESSION" -n services -c "$PROJECT_DIR"

  user_pane="$(tmux display-message -p -t "$SESSION:services.0" '#{pane_id}')"
  gateway_pane="$(tmux split-window -h -P -F '#{pane_id}' -t "$user_pane" -c "$PROJECT_DIR")"
  order_pane="$(tmux split-window -v -P -F '#{pane_id}' -t "$user_pane" -c "$PROJECT_DIR")"
  tmux split-window -v -t "$gateway_pane" -c "$PROJECT_DIR"

  tmux send-keys -t "$user_pane" 'make run-user-service' C-m
  tmux send-keys -t "$order_pane" 'make run-order-service' C-m
  tmux send-keys -t "$gateway_pane" 'make run-gateway' C-m
fi

if [ -n "${TMUX:-}" ]; then
  tmux switch-client -t "$SESSION"
else
  tmux attach-session -t "$SESSION"
fi

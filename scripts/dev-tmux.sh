#!/usr/bin/env bash

set -e

SESSION="ai-gateway-lab"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

if tmux has-session -t "$SESSION" 2>/dev/null; then
  echo "tmux session already exists: $SESSION"
else
  tmux new-session -d -s "$SESSION" -n services -c "$PROJECT_DIR"

  mock_a_pane="$(tmux display-message -p -t "$SESSION:services.0" '#{pane_id}')"
  gateway_pane="$(tmux split-window -h -P -F '#{pane_id}' -t "$mock_a_pane" -c "$PROJECT_DIR")"
  mock_b_pane="$(tmux split-window -v -P -F '#{pane_id}' -t "$mock_a_pane" -c "$PROJECT_DIR")"

  tmux send-keys -t "$mock_a_pane" 'make run-mock-a' C-m
  tmux send-keys -t "$mock_b_pane" 'make run-mock-b' C-m
  tmux send-keys -t "$gateway_pane" 'make run-gateway' C-m
  tmux select-layout -t "$SESSION:services" tiled
fi

if [ -n "${TMUX:-}" ]; then
  tmux switch-client -t "$SESSION"
else
  tmux attach-session -t "$SESSION"
fi

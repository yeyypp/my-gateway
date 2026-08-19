#!/usr/bin/env bash

set -e

SESSION="ai-gateway-lab"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

if tmux has-session -t "$SESSION" 2>/dev/null; then
  echo "tmux session already exists: $SESSION"
else
  tmux new-session -d -s "$SESSION" -n services -c "$PROJECT_DIR"

  gateway_pane="$(tmux display-message -p -t "$SESSION:services.0" '#{pane_id}')"
  command_pane="$(tmux split-window -h -p 40 -P -F '#{pane_id}' -t "$gateway_pane" -c "$PROJECT_DIR")"
  mock_b_pane="$(tmux split-window -v -p 33 -P -F '#{pane_id}' -t "$gateway_pane" -c "$PROJECT_DIR")"
  mock_a_pane="$(tmux split-window -v -p 50 -P -F '#{pane_id}' -t "$gateway_pane" -c "$PROJECT_DIR")"

  tmux send-keys -t "$mock_a_pane" 'make run-mock-a' C-m
  tmux send-keys -t "$mock_b_pane" 'make run-mock-b' C-m
  tmux send-keys -t "$gateway_pane" 'make run-gateway' C-m
  tmux select-pane -t "$command_pane"
fi

if [ -n "${TMUX:-}" ]; then
  tmux switch-client -t "$SESSION"
else
  tmux attach-session -t "$SESSION"
fi

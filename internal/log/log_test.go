package log

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestDefaultLoggerIsSafeBeforeInit(t *testing.T) {
	Info("info before init", zap.String("key", "value"))
	Error("error before init", zap.Error(errors.New("test error")))

	if err := Sync(); err != nil {
		t.Fatalf("sync default logger: %v", err)
	}
}

func TestStructuredFieldsAndCaller(t *testing.T) {
	previousLogger := logger
	t.Cleanup(func() {
		logger = previousLogger
	})

	logPath := t.TempDir() + "/gateway.log"
	if err := Init("info", logPath); err != nil {
		t.Fatalf("initialize logger: %v", err)
	}

	Info("request completed",
		zap.String("logical_model", "model-a"),
		zap.Int("status", 200),
	)
	if err := Sync(); err != nil {
		t.Fatalf("sync logger: %v", err)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log output: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(content, &entry); err != nil {
		t.Fatalf("decode log output: %v", err)
	}
	if got := entry["logical_model"]; got != "model-a" {
		t.Fatalf("expected logical_model field %q, got %#v", "model-a", got)
	}
	if got := entry["status"]; got != float64(200) {
		t.Fatalf("expected status field %d, got %#v", 200, got)
	}
	caller, ok := entry["caller"].(string)
	if !ok || !strings.Contains(caller, "log_test.go") {
		t.Fatalf("expected caller to reference log_test.go, got %#v", entry["caller"])
	}
}

.PHONY: help build dev run-gateway run-mock-a run-mock-b fmt test clean

BIN_DIR := bin

help:
	@echo "make build              Build all services"
	@echo "make dev                Start the local tmux development environment"
	@echo "make run-gateway        Run the gateway on port 8080"
	@echo "make run-mock-a         Run Mock A on port 8091"
	@echo "make run-mock-b         Run Mock B on port 8092"
	@echo "make fmt                Format Go files"
	@echo "make test               Run Go tests"
	@echo "make clean              Remove bin/"

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/gateway ./cmd/gateway
	go build -o $(BIN_DIR)/mock-upstream ./cmd/mock-upstream
	go build -o $(BIN_DIR)/study-assistant ./cmd/study-assistant

dev:
	./scripts/dev-tmux.sh

kill:
	tmux kill-session -t ai-gateway-lab

run-gateway:
	go run ./cmd/gateway start

run-mock-a:
	go run ./cmd/mock-upstream -name mock-a -model model-a -addr :8091

run-mock-b:
	go run ./cmd/mock-upstream -name mock-b -model model-b -addr :8092

fmt:
	go fmt ./...

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)

.PHONY: help build dev run-gateway run-user-service run-order-service fmt test clean

BIN_DIR := bin

help:
	@echo "make build              Build all services"
	@echo "make dev                Start the local tmux development environment"
	@echo "make run-gateway        Run the gateway on port 8080"
	@echo "make run-user-service   Run the user service on port 8081"
	@echo "make run-order-service  Run the order service on port 8082"
	@echo "make fmt                Format Go files"
	@echo "make test               Run Go tests"
	@echo "make clean              Remove bin/"

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/gateway ./cmd/gateway
	go build -o $(BIN_DIR)/user-service ./cmd/user-service
	go build -o $(BIN_DIR)/order-service ./cmd/order-service

dev:
	./scripts/dev-tmux.sh

run-gateway: 
	go run ./cmd/gateway

run-user-service: 
	go run ./cmd/user-service

run-order-service: 
	go run ./cmd/order-service

fmt:
	go fmt ./...

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)

.PHONY: help build run-gateway run-service-a run-service-b fmt test clean

APP_DIR := ./bin
GATEWAY := $(APP_DIR)/gateway
SERVICE_A := $(APP_DIR)/serviceA
SERVICE_B := $(APP_DIR)/serviceB

help:
	@echo "Available targets:"
	@echo "  make build          Build gateway, serviceA, and serviceB"
	@echo "  make run-gateway    Run the gateway service"
	@echo "  make run-service-a  Run serviceA on port 8081"
	@echo "  make run-service-b  Run serviceB on port 8082"
	@echo "  make fmt            Format Go source files"
	@echo "  make test           Run Go tests"
	@echo "  make clean          Remove build output"

build: $(APP_DIR)
	go build -o $(GATEWAY) ./cmd/gateway
	go build -o $(SERVICE_A) ./cmd/serviceA
	go build -o $(SERVICE_B) ./cmd/serviceB

$(APP_DIR):
	mkdir -p $(APP_DIR)

run-gateway:
	go run ./cmd/gateway

run-service-a:
	go run ./cmd/serviceA

run-service-b:
	go run ./cmd/serviceB

fmt:
	go fmt ./...

test:
	go test ./...

clean:
	rm -rf $(APP_DIR)

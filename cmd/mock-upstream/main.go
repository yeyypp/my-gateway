package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"

	"go.uber.org/zap"
)

var (
	name  string
	addr  string
	model string
)

func main() {
	if err := log.Init("info", "stdout"); err != nil {
		fmt.Fprintf(os.Stderr, "initialize logger: %v\n", err)
		os.Exit(1)
	}

	flag.StringVar(&name, "name", "mock-a", "mock service name")
	flag.StringVar(&addr, "addr", ":8091", "mock service addr")
	flag.StringVar(&model, "model", "model-a", "mock service model")
	flag.Parse()

	config := Config{
		Name:  name,
		Addr:  addr,
		Model: model,
	}
	server := NewMockServer(config)
	log.Info("mock upstream starting",
		zap.String("addr", server.Server.Addr),
	)
	if err := server.Start(); err != nil {
		log.Error("mock upstream stopped", zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	}
	_ = log.Sync()
}

type Config struct {
	Name  string
	Addr  string
	Model string
}

type MockServer struct {
	Name   string
	Model  string
	Server *http.Server
}

func NewMockServer(config Config) *MockServer {
	server := &http.Server{
		Addr: config.Addr,
	}

	mockAPIHandler := handlers.NewMockAPIHandler(config.Name, config.Model)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", mockAPIHandler)
	mux.HandleFunc("GET /readyz", mockAPIHandler.CheckHealth)
	server.Handler = mux

	mockServer := &MockServer{
		Name:   config.Name,
		Model:  config.Model,
		Server: server,
	}
	return mockServer
}

func (mockServer *MockServer) Start() error {
	return mockServer.Server.ListenAndServe()
}

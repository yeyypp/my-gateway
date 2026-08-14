package main

import (
	"flag"
	"log"
	"net/http"
	"shuai/gateway/internal/handlers"
)

var (
	name  string
	addr  string
	model string
)

func main() {
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
	server.Start()
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
	server.Handler = mux

	mockServer := &MockServer{
		Name:   config.Name,
		Model:  config.Model,
		Server: server,
	}
	return mockServer
}

func (mockServer *MockServer) Start() {
	log.Fatal(mockServer.Server.ListenAndServe())
}

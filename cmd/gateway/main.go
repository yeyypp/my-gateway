package main

import (
	"log"
	"net/http"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/proxy"
	"shuai/gateway/internal/router"
	"time"
)

func main() {
	sharedTransport := &http.Transport{
		MaxIdleConns:          5,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}

	modelA, err := proxy.BuildReverseProxy(
		proxy.RoutePolicy{
			UpstreamURL: "http://localhost:8091",
		},
		sharedTransport,
	)
	if err != nil {
		log.Fatalf("Failed to build user reverse proxy: %v", err)
	}

	modelB, err := proxy.BuildReverseProxy(
		proxy.RoutePolicy{
			UpstreamURL: "http://localhost:8092",
		},
		sharedTransport,
	)
	if err != nil {
		log.Fatalf("Failed to build order reverse proxy: %v", err)
	}
	router := router.NewModelRouter(modelA, modelB)

	server := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", middleware.ValidateJSONRequest(middleware.RequestTimeout(5*time.Second)(router)))
	server.Handler = mux
	log.Println("Starting gateway server on :8080")
	log.Fatal(server.ListenAndServe())
}

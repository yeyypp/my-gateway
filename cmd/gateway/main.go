package main

import (
	"log"
	"net/http"
	"shuai/gateway/internal"
	"time"
)

func main() {
	sharedTransport := &http.Transport{
		MaxIdleConns:          5,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}

	userProxy, err := internal.BuildReverseProxy(
		internal.RoutePolicy{
			UpstreamURL: "http://localhost:8081",
		},
		sharedTransport,
	)
	if err != nil {
		log.Fatalf("Failed to build user reverse proxy: %v", err)
		return
	}

	orderProxy, err := internal.BuildReverseProxy(
		internal.RoutePolicy{
			UpstreamURL: "http://localhost:8082",
		},
		sharedTransport,
	)
	if err != nil {
		log.Fatalf("Failed to build order reverse proxy: %v", err)
		return
	}

	server := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	mux := http.NewServeMux()
	mux.Handle("/user/", http.StripPrefix("/user", userProxy))
	mux.Handle("/order/", http.StripPrefix("/order", orderProxy))
	server.Handler = mux
	log.Println("Starting gateway server on :8080")
	log.Fatal(server.ListenAndServe())
}

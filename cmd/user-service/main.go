package main

import (
	"fmt"
	"log"
	"net/http"
	"shuai/gateway/internal/api"
	"time"
)

func main() {
	server := &http.Server{
		Addr:         ":8081",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", InfoHandler)
	mux.HandleFunc("POST /process", ProcessHandler)
	server.Handler = mux

	log.Fatal(server.ListenAndServe())

}

func InfoHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "this is service user")
}

func ProcessHandler(w http.ResponseWriter, r *http.Request) {
	api.ProcessSimpleRequest(w, r)
}

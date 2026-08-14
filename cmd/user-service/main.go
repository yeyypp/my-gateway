package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"shuai/gateway/internal/handlers"
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
	ProcessSimpleRequest(w, r)
}

func ProcessSimpleRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode    string `json:"mode"`
		DelayMs int    `json:"delay_ms"`
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		handlers.ResponseJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err), nil)
		return
	}

	switch req.Mode {
	case "ok":
		if req.DelayMs != 0 {
			timer := time.NewTimer(time.Duration(req.DelayMs) * time.Millisecond)
			defer timer.Stop()

			select {
			case <-timer.C:
			case <-r.Context().Done():
				// timeout
				return
			}
		}
		handlers.ResponseJSON(w, http.StatusOK, "ok", nil)
	case "bad_request":
		handlers.ResponseJSON(w, http.StatusBadRequest, "upstream bad request", nil)
	case "internal_error":
		handlers.ResponseJSON(w, http.StatusInternalServerError, "upstream internal server error", nil)
	default:
		handlers.ResponseJSON(w, http.StatusBadRequest, fmt.Sprintf("unknown mode: %s", req.Mode), nil)
	}
}

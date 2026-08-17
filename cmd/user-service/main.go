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
	handlers.SendJSON(w, map[string]string{"message": "this is service user"})
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
		handlers.SendError(w, handlers.APIError{
			Message: fmt.Sprintf("invalid request body: %v", err),
			Type:    "invalid_request_error",
			Code:    "invalid_json",
		}, http.StatusBadRequest)
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
		handlers.SendJSON(w, map[string]string{"message": "ok"})
	case "bad_request":
		handlers.SendError(w, handlers.APIError{
			Message: "upstream bad request",
			Type:    "invalid_request_error",
			Code:    "bad_request",
		}, http.StatusBadRequest)
	case "internal_error":
		handlers.SendError(w, handlers.APIError{
			Message: "upstream internal server error",
			Type:    "server_error",
			Code:    "internal_error",
		}, http.StatusInternalServerError)
	default:
		handlers.SendError(w, handlers.APIError{
			Message: fmt.Sprintf("unknown mode: %s", req.Mode),
			Type:    "invalid_request_error",
			Param:   "mode",
			Code:    "unsupported_value",
		}, http.StatusBadRequest)
	}
}

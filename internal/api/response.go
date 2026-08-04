package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type JsonResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func ResponseJSON(w http.ResponseWriter, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := JsonResponse{
		Code:    code,
		Message: message,
		Data:    data,
	}
	json.NewEncoder(w).Encode(resp)
}

func ProcessSimpleRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		ResponseJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err), nil)
		return
	}

	switch req.Mode {
	case "ok":
		ResponseJSON(w, http.StatusOK, "ok", nil)
	case "bad_request":
		ResponseJSON(w, http.StatusBadRequest, "upstream bad request", nil)
	case "internal_error":
		ResponseJSON(w, http.StatusInternalServerError, "upstream internal server error", nil)
	default:
		ResponseJSON(w, http.StatusBadRequest, fmt.Sprintf("unknown mode: %s", req.Mode), nil)
	}
}

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"shuai/gateway/internal/protocol"
)

func SendJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "json encoded resp error:%v", err)
	}
}

func SendJSONWithStatusCode(w http.ResponseWriter, data any, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "json encoded resp error:%v", err)
	}
}

func SendError(w http.ResponseWriter, err protocol.APIError, statusCode int) {
	errResp := protocol.ErrorResponse{
		Error: err,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "json encoded resp error:%v", err)
	}
}

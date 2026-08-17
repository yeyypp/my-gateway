package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

func SendJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "json encoded resp error:%v", err)
	}
}

func SendError(w http.ResponseWriter, err APIError, statusCode int) {
	errResp := ErrorResponse{
		Error: err,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "json encoded resp error:%v", err)
	}
}

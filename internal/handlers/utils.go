package handlers

import (
	"encoding/json"
	"net/http"

	"shuai/gateway/internal/log"
	"shuai/gateway/internal/protocol"

	"go.uber.org/zap"
)

func SendJSON(w http.ResponseWriter, data any) {
	sendJSON(w, data, http.StatusOK)
}

func SendError(w http.ResponseWriter, err protocol.APIError, statusCode int) {
	fallback := protocol.ErrorForStatus(statusCode)
	if err.Message == "" {
		err.Message = fallback.Message
	}
	if err.Type == "" {
		err.Type = fallback.Type
	}
	sendJSON(w, protocol.ErrorResponse{Error: err}, statusCode)
}

func sendJSON(w http.ResponseWriter, data any, statusCode int) {
	body, err := json.Marshal(data)
	if err != nil {
		log.Error("encode JSON response failed", zap.Error(err))
		statusCode = http.StatusInternalServerError
		body, _ = json.Marshal(protocol.ErrorResponse{Error: protocol.ErrorForStatus(statusCode)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if _, err := w.Write(append(body, '\n')); err != nil {
		log.Error("write JSON response failed", zap.Error(err))
	}
}

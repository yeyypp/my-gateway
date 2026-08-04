package internal

import (
	"encoding/json"
	"net/http"
	"shuai/gateway/internal/api"
)

func ValidateJSONRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		var req struct {
			Mode string `json:"mode"`
		}

		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			api.ResponseJSON(w, http.StatusBadRequest, "invalid request body", nil)
			return
		}

		
	})
}

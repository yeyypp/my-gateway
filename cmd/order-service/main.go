package main

import (
	"net/http"
	"shuai/gateway/internal/handlers"
)

func main() {
	http.HandleFunc("/info", InfoHandler)
	http.ListenAndServe(":8082", nil)
}

func InfoHandler(w http.ResponseWriter, r *http.Request) {
	handlers.SendJSON(w, map[string]string{"message": "this is service order"})
}

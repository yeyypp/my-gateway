package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/info", InfoHandler)
	http.ListenAndServe(":8082", nil)
}

func InfoHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "this is service b")
}

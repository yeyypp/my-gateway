package main

import (
	"net/http"
	"strings"
)

func main() {

}

type ShuaiGateWay struct {
}

func (sgw *ShuaiGateWay) ServerHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch parsePath(path) {
	case "user":
	case "order":
	default:
		http.NotFound(w, r)
		return
	}
}

func routeUser(r *http.Request) {

}

func parsePath(path string) string {
	if strings.Contains(path, "user") {
		return "user"
	}
	return "order"
}

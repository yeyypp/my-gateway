package main

import (
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	USER_SERVICE  = "http://localhost:8081/info"
	ORDER_SERVICE = "http://localhost:8082/info"
)

func main() {
	sgw := NewSGW()
	http.Handle("/", sgw)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

type ShuaiGateWay struct {
	client *http.Client
}

func NewSGW() *ShuaiGateWay {
	client := &http.Client{
		Timeout: 15 * time.Second,

		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   3 * time.Second,
				KeepAlive: 10 * time.Second,
			}).DialContext,

			MaxIdleConns:        5,
			MaxIdleConnsPerHost: 2,

			IdleConnTimeout: 5 * time.Second,
		},
	}

	return &ShuaiGateWay{
		client: client,
	}
}

func (sgw *ShuaiGateWay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("|Method:%s |Path:%s", r.Method, r.URL)
	path := r.URL.Path
	switch parsePath(path) {
	case "user":
		err := sgw.routeService("user", w, r)
		if err != nil {
			log.Printf("route user failed.\n")
			return
		}
	case "order":
		err := sgw.routeService("order", w, r)
		if err != nil {
			log.Printf("route order failed.\n")
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
}

func (sgw *ShuaiGateWay) routeService(service string, w http.ResponseWriter, r *http.Request) error {
	var newPath string
	switch service {
	case "user":
		newPath = USER_SERVICE
	case "order":
		newPath = ORDER_SERVICE
	default:
		http.NotFound(w, r)
		return nil
	}

	newReq, err := http.NewRequestWithContext(r.Context(), r.Method, newPath, r.Body)
	if err != nil {
		log.Printf("new req failed.\n")
		http.Error(w, "newReq failed", http.StatusInternalServerError)
		return err
	}

	newReq.Header = r.Header.Clone()
	newReq.URL.RawQuery = r.URL.RawQuery
	resp, err := sgw.client.Do(newReq)
	if err != nil {
		log.Printf("get resp failed.\n")
		http.Error(w, "get resp failed", http.StatusBadGateway)
		return err
	}
	defer resp.Body.Close()

	maps.Copy(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("copy resp body failed.\n")
		return err
	}
	return nil
}

func parsePath(path string) string {
	if strings.Contains(path, "user") {
		return "user"
	}
	if strings.Contains(path, "order") {
		return "order"
	}
	return ""
}

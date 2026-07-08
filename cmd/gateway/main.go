package main

import "net/http"

func main() {

}

type ShuaiGateWay struct {
}

func (sgw *ShuaiGateWay) ServerHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.Method	

}

//go:build routing

// Command echo is the contract test's backend: it reports the request exactly as the proxy
// delivered it. Test-only; never part of the product binary.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type report struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Pad     string              `json:"pad,omitempty"`
}

func handler(w http.ResponseWriter, r *http.Request) {
	rep := report{Method: r.Method, Path: r.URL.Path, Headers: r.Header}
	if n, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && n > 0 && n <= 1<<20 {
		rep.Pad = strings.Repeat("a", n)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rep); err != nil {
		log.Printf("encode: %v", err)
	}
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(handler), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

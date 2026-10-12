//go:build routing

// Command stub-forwardauth imitates the Authelia forward-auth contract the edge relies on:
// session cookie "mm_stub_session" is "one" or "two" (authentication factors); a missing session
// is refused, and so is a one-factor session on a path in STUB_TWO_FACTOR (newline separated
// regexps, from deploy/authelia-rules.example.yml). Both refusals use Authelia 4.39's
// getAuthzRedirectStatusCode: 401 for XHR or when Accept lacks text/html, else 302 for
// GET/HEAD/OPTIONS and 303 for other methods. 403 is only for a deny policy, which the
// fixture has none of.
// GET /__calls returns the number of /verify calls. Test-only.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

type stub struct {
	user  string
	two   []*regexp.Regexp
	calls atomic.Int64
}

func (s *stub) session(r *http.Request) string {
	if c, err := r.Cookie("mm_stub_session"); err == nil && (c.Value == "one" || c.Value == "two") {
		return c.Value
	}
	return ""
}

func (s *stub) needsTwo(uri string) bool {
	for _, re := range s.two {
		if re.MatchString(uri) {
			return true
		}
	}
	return false
}

// refuse mirrors Authelia's redirect status decision for unauthenticated and step-up requests.
func refuse(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	code := http.StatusSeeOther
	switch r.Header.Get("X-Forwarded-Method") {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		code = http.StatusFound
	}
	http.Redirect(w, r, "https://auth.example.com/?rd=login", code)
}

func (s *stub) verify(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	uri := r.Header.Get("X-Forwarded-Uri")
	switch sess := s.session(r); {
	case sess == "", s.needsTwo(uri) && sess != "two":
		refuse(w, r)
	default:
		w.Header().Set("Remote-User", s.user)
		w.Header().Set("Remote-Groups", "admins")
		w.Header().Set("Remote-Name", "Stub User")
		w.Header().Set("Remote-Email", "stub@example.com")
		w.WriteHeader(http.StatusOK)
	}
}

func main() {
	s := &stub{user: os.Getenv("STUB_USER")}
	for _, p := range strings.Split(os.Getenv("STUB_TWO_FACTOR"), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			s.two = append(s.two, regexp.MustCompile(p))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/verify", s.verify)
	mux.HandleFunc("/__calls", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, s.calls.Load()) })
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":9091"
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

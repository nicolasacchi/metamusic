package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// servedRecorder remembers which operations were served to SPA-tagged requests.
type servedRecorder struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (s *servedRecorder) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = map[string]struct{}{}
	}
	s.ids[id] = struct{}{}
}

func (s *servedRecorder) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.ids))
	for id := range s.ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func (s *servedRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s.list())
	})
}

func isSPARequest(r *http.Request) bool { return reqinfo.IsSPARequest(r) }

// leak is one canary sighting; it never carries the value.
type leak struct {
	Path   string `json:"path"`
	Method string `json:"method"`
	Kind   string `json:"kind"`
	Where  string `json:"where"` // "header" | "body"
}

type canary struct {
	kind string
	val  []byte
}

// leakScanner (C39, only with Cfg.E2E) looks for the run's static secrets in
// every response: headers always, bodies except under /media/ (audio bytes
// are opaque to JSON canaries and large).
type leakScanner struct {
	canaries []canary
	window   int

	mu    sync.Mutex
	found []leak
}

func newLeakScanner(sec config.Secrets) *leakScanner {
	ls := &leakScanner{}
	add := func(kind string, v []byte) {
		if len(v) == 0 {
			return
		}
		ls.canaries = append(ls.canaries, canary{kind, v})
		ls.window = max(ls.window, len(v))
	}
	if k := sec.Key; len(k) > 0 {
		add("secret_key", k)
		add("secret_key", []byte(hex.EncodeToString(k)))
		add("secret_key", []byte(base64.RawStdEncoding.EncodeToString(k)))
		add("secret_key", []byte(base64.RawURLEncoding.EncodeToString(k)))
	}
	add("edge_secret", []byte(sec.EdgeSecret))
	add("ntfy_token", []byte(sec.NtfyToken))
	return ls
}

func (ls *leakScanner) record(r *http.Request, kind, where string) {
	l := leak{Path: r.URL.Path, Method: r.Method, Kind: kind, Where: where}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if !slices.Contains(ls.found, l) {
		ls.found = append(ls.found, l)
	}
}

type leakWriter struct {
	http.ResponseWriter
	ls       *leakScanner
	r        *http.Request
	scanBody bool
	carry    []byte
	headers  bool
}

func (w *leakWriter) scanHeaders() {
	if w.headers {
		return
	}
	w.headers = true
	for k, vs := range w.Header() {
		for _, v := range vs {
			for _, c := range w.ls.canaries {
				if strings.Contains(v, string(c.val)) || strings.Contains(k, string(c.val)) {
					w.ls.record(w.r, c.kind, "header")
				}
			}
		}
	}
}

func (w *leakWriter) WriteHeader(code int) {
	w.scanHeaders()
	w.ResponseWriter.WriteHeader(code)
}

func (w *leakWriter) Write(b []byte) (int, error) {
	w.scanHeaders()
	if w.scanBody {
		buf := append(append([]byte(nil), w.carry...), b...)
		for _, c := range w.ls.canaries {
			if bytes.Contains(buf, c.val) {
				w.ls.record(w.r, c.kind, "body")
			}
		}
		keep := min(w.ls.window-1, len(buf))
		w.carry = append(w.carry[:0], buf[len(buf)-keep:]...)
	}
	return w.ResponseWriter.Write(b)
}

func (w *leakWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *leakWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (ls *leakScanner) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&leakWriter{ResponseWriter: w, ls: ls, r: r, scanBody: !strings.HasPrefix(r.URL.Path, "/media/")}, r)
	})
}

func (ls *leakScanner) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		out := append([]leak{}, ls.found...)
		ls.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

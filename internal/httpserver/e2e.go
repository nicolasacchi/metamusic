package httpserver

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
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

func isSPARequest(r *http.Request) bool {
	return r.Header.Get("X-MM-Client") == "spa" || r.URL.Query().Get("mm_client") == "spa"
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

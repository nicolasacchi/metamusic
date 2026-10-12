package mcpgen

import (
	"encoding/json"
	"mime"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/api"
)

// problemWriter rewrites non-JSON error responses of the SDK transport (405,
// malformed payloads, ...) into application/problem+json, so /mcp errors keep
// the {code,message,details} shape. JSON responses (JSON-RPC errors, tool
// results) pass through untouched. The SDK's text is dropped: it may echo
// request content.
type problemWriter struct {
	http.ResponseWriter
	wrote   bool
	discard bool
}

func isJSON(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || mt == "application/problem+json")
}

func (w *problemWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if code >= 400 && !isJSON(w.Header().Get("Content-Type")) {
		w.discard = true
		h := w.Header()
		h.Set("Content-Type", "application/problem+json")
		h.Del("Content-Length")
		w.ResponseWriter.WriteHeader(code)
		_ = json.NewEncoder(w.ResponseWriter).Encode(map[string]any{
			"code": api.CodeFor(code), "message": http.StatusText(code), "details": map[string]any{},
		})
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *problemWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.discard {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *problemWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok && !w.discard {
		f.Flush()
	}
}

func (w *problemWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func problemJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&problemWriter{ResponseWriter: w}, r)
	})
}

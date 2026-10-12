package httpserver

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const csp = "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"

func htmlHeaders(h http.Header) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

const notBuilt = `<!doctype html><html><head><meta charset="utf-8"><title>metamusic</title></head>
<body><h1>UI not built</h1><p>The web UI is not embedded in this binary. Run <code>make web</code> and rebuild.</p></body></html>
`

func spaHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if fsys != nil {
			name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
			if name != "" && name != "index.html" {
				if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
					if strings.HasPrefix(name, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					w.Header().Set("X-Content-Type-Options", "nosniff")
					http.ServeFileFS(w, r, fsys, name)
					return
				}
			}
			if b, err := fs.ReadFile(fsys, "index.html"); err == nil {
				htmlHeaders(w.Header())
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				if r.Method == http.MethodGet {
					_, _ = w.Write(b)
				}
				return
			}
		}
		htmlHeaders(w.Header())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(notBuilt))
		}
	})
}

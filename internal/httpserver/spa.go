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
					if name == "manifest.webmanifest" {
						// The host MIME table decides otherwise (C20).
						w.Header().Set("Content-Type", "application/manifest+json")
					}
					if strings.HasSuffix(name, ".html") {
						htmlHeaders(w.Header())
					}
					http.ServeFileFS(w, r, fsys, name)
					return
				}
			}
			if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "icons/") {
				// A missing static file must not fall back to the SPA shell.
				http.NotFound(w, r)
				return
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

// swHandler serves the service worker script from the SPA filesystem (C114, D-M4a-18): a classic script with
// Content-Type text/javascript and Cache-Control no-cache, so the browser revalidates it on every update check.
// A missing script is a 404 problem, never the HTML shell (a browser would try to run it as a script).
func swHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if fsys == nil {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
			return
		}
		b, err := fs.ReadFile(fsys, "sw.js")
		if err != nil {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	})
}

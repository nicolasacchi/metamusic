package serve

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

var mediaPath = regexp.MustCompile(`^/media/([0-9a-f]{64})\.(flac|mp3)$`)

// Handler serves signed /media/<sha>.<ext> links (C18). data is the DataDir
// root; probe, when non-nil, is the device-probe object addressed by its ETag
// (its sha) and served without consulting res.
func Handler(s *Signer, data *os.Root, res Resolver, probe *Object) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			problem.Write(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
			return
		}
		m := mediaPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			problem.Write(w, http.StatusNotFound, "NOT_FOUND", "no such resource", nil)
			return
		}
		sha, ext := m[1], m[2]
		q := r.URL.Query()
		switch err := s.Verify(sha, ext, q.Get("exp"), q.Get("sig")); {
		case errors.Is(err, ErrLinkExpired):
			problem.Write(w, http.StatusForbidden, "MEDIA_LINK_EXPIRED", "media link expired", nil)
			return
		case err != nil:
			problem.Write(w, http.StatusForbidden, "MEDIA_LINK_INVALID", "media link invalid", nil)
			return
		}

		var obj Object
		if probe != nil && ext == "flac" && sha == probe.ETag {
			obj = *probe
		} else {
			o, err := res.Resolve(r.Context(), sha, ext)
			switch {
			case errors.Is(err, ErrNotFound):
				problem.Write(w, http.StatusNotFound, "NOT_FOUND", "no such resource", nil)
				return
			case err != nil:
				internal(w, sha, ext, err)
				return
			}
			obj = o
		}
		if data == nil {
			internal(w, sha, ext, errors.New("no data root"))
			return
		}
		f, err := data.Open(obj.RelPath)
		if err != nil {
			internal(w, sha, ext, err)
			return
		}
		defer func() { _ = f.Close() }()
		if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
			if err == nil {
				err = errors.New("not a regular file")
			}
			internal(w, sha, ext, err)
			return
		}

		h := w.Header()
		h.Set("Content-Type", obj.ContentType)
		h.Set("Accept-Ranges", "bytes")
		h.Set("ETag", `"`+obj.ETag+`"`)
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		http.ServeContent(w, r, "", obj.ModTime, f)
	})
}

func internal(w http.ResponseWriter, sha, ext string, err error) {
	// err can embed the file path (inside DataDir) but never the request query.
	slog.Error("media: serving failed", "sha", sha, "ext", ext, "err", strings.TrimSpace(err.Error()))
	problem.Write(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
}

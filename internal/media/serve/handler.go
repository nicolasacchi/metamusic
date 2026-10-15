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

var mediaPath = regexp.MustCompile(`^/media/([0-9a-f]{64})\.(flac|mp3|jsonl|zip|jpg|png)$`)

// downloadName is the ASCII file name a Content-Disposition may carry (no RFC 5987 form needed).
var downloadName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,119}$`)

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
		serveObject(w, r, data, obj, "private, max-age=31536000, immutable", "sha", sha, "ext", ext)
	})
}

func internal(w http.ResponseWriter, sha, ext string, err error) {
	// err can embed the file path (inside DataDir) but never the request query.
	slog.Error("media: serving failed", "sha", sha, "ext", ext, "err", strings.TrimSpace(err.Error()))
	problem.Write(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
}

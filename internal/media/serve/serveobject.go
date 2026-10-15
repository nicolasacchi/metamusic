package serve

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// ServeObject writes obj (a file under data) with the media response headers (C110.3): Content-Type,
// Accept-Ranges, ETag, cacheControl, nosniff, Referrer-Policy no-referrer, CORP same-origin,
// Content-Disposition when obj.DownloadName is a valid ASCII name, then http.ServeContent (Range, HEAD,
// conditional requests). A missing, unreadable or non-regular file answers 500 INTERNAL, logged with the
// relative path only. The signature and method checks belong to the caller.
func ServeObject(w http.ResponseWriter, r *http.Request, data *os.Root, obj Object, cacheControl string) {
	serveObject(w, r, data, obj, cacheControl)
}

// serveObject is ServeObject with extra log attributes for the failure line (the handler adds sha and ext).
func serveObject(w http.ResponseWriter, r *http.Request, data *os.Root, obj Object, cacheControl string, logAttrs ...any) {
	if data == nil {
		objectFailed(w, obj.RelPath, errors.New("no data root"), logAttrs)
		return
	}
	f, err := data.Open(obj.RelPath)
	if err != nil {
		objectFailed(w, obj.RelPath, err, logAttrs)
		return
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		objectFailed(w, obj.RelPath, err, logAttrs)
		return
	}
	h := w.Header()
	h.Set("Content-Type", obj.ContentType)
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", `"`+obj.ETag+`"`)
	h.Set("Cache-Control", cacheControl)
	if downloadName.MatchString(obj.DownloadName) {
		h.Set("Content-Disposition", `attachment; filename="`+obj.DownloadName+`"`)
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, "", obj.ModTime, f)
}

func objectFailed(w http.ResponseWriter, rel string, err error, attrs []any) {
	// err can embed the file path (inside DataDir) but never the request query.
	slog.Error("media: serving failed", append(attrs, "rel", rel, "err", err.Error())...)
	problem.Write(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
}

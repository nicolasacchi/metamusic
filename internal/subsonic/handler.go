// Package subsonic is the OpenSubsonic protocol adapter over the library services (C108, C109). It is not part
// of the registry (its shape is mandated by the protocol); its writes go through service-level audit.
//
// The exported API is frozen by M4a-B2. NewHandler is STUB(M4a-S1): it answers protocol error 0 for every
// endpoint until the Subsonic lane implements the protocol layer.
package subsonic

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// CoverSource renders covers; *cover.Service in production, fakes in lane tests.
type CoverSource interface {
	EnsureTrack(ctx context.Context, trackID string, size int) (serve.Object, string, error)
	EnsureAlbum(ctx context.Context, rootID string, size int) (serve.Object, string, error)
}

// DownloadSource builds tagged downloads; *tagged.Service in production, fakes in lane tests.
type DownloadSource interface {
	Ensure(ctx context.Context, trackID string) (tagged.Download, error)
}

// Options are the handler's collaborators (C108).
type Options struct {
	Cfg       config.Config
	Version   string       // serverVersion
	Store     *store.Store // read pool for catalog SQL (C109.1)
	Data      *os.Root
	Lib       *library.Service
	Judgments *judgments.Service
	Playlists *playlists.Service
	Listening *listening.Service
	Covers    CoverSource
	Downloads DownloadSource
	Creds     *auth.SubsonicStore
	Audit     *audit.Store
	Limiter   *ratelimit.Limiter
	Now       func() time.Time
	Rand      func() uint64 // getRandomSongs / getAlbumList type=random; tests inject a fixed sequence
	FailDelay time.Duration // default 1 s
	Sleep     func(ctx context.Context, d time.Duration) error
}

// NewHandler returns the handler mounted by httpserver at /rest/. STUB(M4a-S1): every request, whatever the
// method or endpoint, answers HTTP 200 with a failed envelope, error 0 "unsupported endpoint".
func NewHandler(o Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := Response{XMLNS: xmlNamespace, Status: "failed", Version: APIVersion, Type: "metamusic", ServerVersion: o.Version,
			OpenSubsonic: true, Error: &ErrorBody{Code: 0, Message: "unsupported endpoint"}}
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.URL.Query().Get("f") == "json" {
			h.Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]Response{"subsonic-response": resp})
			return
		}
		h.Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(xml.Header))
		_ = xml.NewEncoder(w).Encode(resp)
	})
}

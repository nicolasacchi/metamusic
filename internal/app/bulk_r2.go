package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/upload/fallback"
)

// The BULK-R2 upload fallback wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build). It is off unless
// <secrets>/r2.env exists: with the file absent nothing is built, no loop runs, no client is made and the runner is untouched, and
// get_provider_status says "not configured". r2.env is read once per Build (rotation is a restart, D-BULK-19).
func init() { RegisterBuildHook(wireUploadFallback) }

// fallbackHTTPTimeout bounds one bucket request; the largest object a worker may put is 160 MiB.
const fallbackHTTPTimeout = 10 * time.Minute

// fallbackStatus is the read side get_provider_status uses.
type fallbackStatus struct {
	env        *BuildEnv
	configured bool
}

func (s fallbackStatus) Status(ctx context.Context) (bool, int, *time.Time, error) {
	st, err := fallback.ReadStatus(ctx, s.env.Store.R, s.configured)
	return st.Configured, st.ObjectsPending, st.LastUsedAt, err
}

func wireUploadFallback(env *BuildEnv) error {
	var cfg config.UploadFallback
	ok := false
	if env.Cfg.SecretsDir != "" { // an empty directory would make the loader look in the working directory
		var err error
		if cfg, ok, err = config.LoadUploadFallback(env.Cfg.SecretsDir); err != nil {
			return err // a broken r2.env stops startup naming the variable, never its value (deploy safety)
		}
	}
	env.Services.Deps.UploadFallback = fallbackStatus{env: env, configured: ok}
	if !ok || env.Services.Uploads == nil {
		return nil
	}
	hc := env.Options.FallbackHTTP
	if hc == nil {
		hc = &http.Client{Timeout: fallbackHTTPTimeout}
	}
	client, err := objstore.New(objstore.Config{Endpoint: cfg.Endpoint, Bucket: cfg.Bucket, Region: cfg.Region, AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey},
		hc, env.Now)
	if err != nil {
		return err
	}
	// Options.AfterS stays at fallback.DefaultAfterS until BULK-P1 wires MM_FALLBACK_AFTER_S through here (C399.4).
	fb, err := fallback.New(fallback.Options{Store: env.Store, Uploads: env.Services.Uploads, Objects: client, Prefix: cfg.Prefix, Clock: env.Options.M3.Clock, Log: slog.Default()}) // a rig's virtual clock; nil is the real clock
	if err != nil {
		return err
	}
	if r := env.Services.Runner; r != nil {
		r.UseFallback(fb, fb)
	}
	env.AddLoop(Loop{Name: "upload-fallback", Run: fb.Run})
	return nil
}

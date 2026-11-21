package api

import (
	"context"
	"log/slog"
	"time"
)

// UploadFallbackReader is what get_provider_status reads about the upload fallback (BULK-R2). It never touches the network.
type UploadFallbackReader interface {
	Status(ctx context.Context) (configured bool, objectsPending int, lastUsedAt *time.Time, err error)
}

// UploadFallbackStatus is the upload_fallback block of get_provider_status: whether a private bucket is configured, how many of its
// objects are still to be deleted and when a fallback receipt last committed. It carries no endpoint, bucket, prefix, key or URL.
type UploadFallbackStatus struct {
	Configured     bool    `json:"configured"`
	ObjectsPending int     `json:"objects_pending"`
	LastUsedAt     *string `json:"last_used_at"`
}

// uploadFallbackStatus renders the block; a missing reader or a read fault answers "unconfigured, nothing pending" rather than
// failing the whole status.
func (d Deps) uploadFallbackStatus(ctx context.Context) UploadFallbackStatus {
	if d.UploadFallback == nil {
		return UploadFallbackStatus{}
	}
	configured, pending, last, err := d.UploadFallback.Status(ctx)
	if err != nil {
		slog.Warn("upload fallback status unavailable")
		return UploadFallbackStatus{Configured: configured}
	}
	return UploadFallbackStatus{Configured: configured, ObjectsPending: pending, LastUsedAt: tsPtr(last)}
}

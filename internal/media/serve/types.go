// Package serve holds the signed media URL scheme (C18). The Range handler
// lives in handler.go (M1-B5); this file owns the types the library resolves.
package serve

import (
	"context"
	"errors"
	"time"
)

// Object is one servable file: DataDir-relative path plus response metadata.
type Object struct {
	RelPath     string
	ContentType string
	ETag        string // sha256 of the file, hex (the handler quotes it)
	ModTime     time.Time
	// DownloadName, when set, makes the handler send Content-Disposition: attachment
	// (ASCII [a-z0-9._-]). Empty for library objects.
	DownloadName string
}

// Resolver maps a (sha, ext) address to a stored object.
type Resolver interface {
	Resolve(ctx context.Context, sha, ext string) (Object, error)
}

// ErrNotFound is the Resolver result for unknown objects (404).
var ErrNotFound = errors.New("serve: no such object")

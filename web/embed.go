// Package web embeds the built Svelte SPA.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the SPA build output rooted at dist/.
func Dist() fs.FS {
	f, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is a compile-time embedded constant path
	}
	return f
}

//go:build tools

// Package deps pins M0 dependencies in go.mod so `go mod tidy` keeps them
// before the lanes that use them land.
package deps

import (
	_ "github.com/danielgtaylor/huma/v2"
	_ "github.com/danielgtaylor/huma/v2/adapters/humago"
	_ "github.com/google/go-cmp/cmp"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/image/font/basicfont"
	_ "modernc.org/sqlite"
)

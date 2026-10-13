package exports_test

import (
	"os"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// parseWithABC parses a raw request whose abc_path resolves to abc.
func parseWithABC(raw []byte, abc string) (yue2.Parsed, error) {
	return yue2.ParseRequest(raw, yue2.ParseOptions{ResolveABCPath: func(string) ([]byte, error) {
		if abc == "" {
			return nil, os.ErrNotExist
		}
		return []byte(abc), nil
	}})
}

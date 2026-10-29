package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackup_ExcludesTapesAndCovers: tape files (14-day derived exports, D-L2-19) and the cover cache are never named by a
// snapshot: only files the database points at are copied, and no table points into derived/tapes/.
func TestBackup_ExcludesTapesAndCovers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "derived/tapes/ab/"+strings.Repeat("ab", 32)+".flac", "x")
	e.write(t, "derived/tapes/.part", "x")
	e.write(t, "derived/covers/ab/abcdef.png", "x")
	_, m, dir := e.run(t)
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "derived/tapes") || strings.HasPrefix(f.Path, "derived/covers") {
			t.Errorf("manifest names %s", f.Path)
		}
	}
	for _, p := range []string{"derived/tapes", "derived/covers"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s exists in the snapshot", p)
		}
	}
}

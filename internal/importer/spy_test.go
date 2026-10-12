package importer

import (
	"io/fs"
	"sort"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
)

// spySource records every file the importer opens.
type spySource struct {
	inner source
	mu    sync.Mutex
	open  map[string]int
}

func newSpy(inner source) *spySource { return &spySource{inner: inner, open: map[string]int{}} }

func (s *spySource) note(p string) {
	s.mu.Lock()
	s.open[p]++
	s.mu.Unlock()
}

func (s *spySource) FS() fs.FS { return spyFS{s, s.inner.FS()} }

func (s *spySource) OpenRegular(rel string, want inbox.FileID) (fs.File, error) {
	s.note(rel)
	return s.inner.OpenRegular(rel, want)
}

func (s *spySource) opened() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.open {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *spySource) wasOpened(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[p] > 0
}

type spyFS struct {
	spy   *spySource
	inner fs.FS
}

func (f spyFS) Open(name string) (fs.File, error) {
	if name != "." {
		f.spy.note(name)
	}
	return f.inner.Open(name)
}
func (f spyFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(f.inner, name) }
func (f spyFS) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(f.inner, name) }
func (f spyFS) Lstat(name string) (fs.FileInfo, error)     { return fs.Lstat(f.inner, name) }
func (f spyFS) ReadLink(name string) (string, error)       { return fs.ReadLink(f.inner, name) }

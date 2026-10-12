// Package inbox is the only way the importer reaches the operator's inbox. It
// wraps a private *os.Root and exposes no write method (C16.0).
package inbox

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// ErrChanged reports that a file is no longer the one the scan saw (replaced,
// resized, or not a regular file any more).
var ErrChanged = errors.New("inbox: file changed since scan")

// FileID is the identity of a file as the scan's Lstat saw it.
type FileID struct {
	Dev, Ino uint64
	Size     int64
	ModTime  time.Time
}

// IDOf extracts the FileID from an Lstat/Stat result. ok is false when the
// platform gives no device/inode numbers.
func IDOf(fi fs.FileInfo) (FileID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}, false
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Size: fi.Size(), ModTime: fi.ModTime()}, true //nolint:unconvert // Dev/Ino widths differ per platform
}

// ReadOnly is a read-only view of the inbox directory.
type ReadOnly struct{ root *os.Root }

// Open opens dir as an inbox.
func Open(dir string) (*ReadOnly, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("inbox: open: %w", err)
	}
	return &ReadOnly{root: r}, nil
}

// FS returns a read-only fs.FS (ReadDir, Stat, Lstat, ReadLink, Open). Files it
// opens expose Read, ReadAt, Seek, Stat, ReadDir and Close only.
func (r *ReadOnly) FS() fs.FS { return roFS{r.root.FS()} }

// OpenRegular opens rel for reading without ever blocking: O_NONBLOCK, no
// symlink following, then fstat must show a regular file whose identity equals
// want, else ErrChanged.
func (r *ReadOnly) OpenRegular(rel string, want FileID) (fs.File, error) {
	f, err := r.root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: symlink", ErrChanged)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	got, ok := IDOf(fi)
	if !fi.Mode().IsRegular() || !ok || got.Dev != want.Dev || got.Ino != want.Ino ||
		got.Size != want.Size || !got.ModTime.Equal(want.ModTime) {
		_ = f.Close()
		return nil, ErrChanged
	}
	return roFile{f}, nil
}

// roFS narrows an fs.FS to read operations and wraps every opened file.
type roFS struct{ inner fs.FS }

func (r roFS) Open(name string) (fs.File, error) {
	f, err := r.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return roFile{f.(*os.File)}, nil
}

func (r roFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(r.inner, name) }
func (r roFS) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(r.inner, name) }
func (r roFS) Lstat(name string) (fs.FileInfo, error)     { return fs.Lstat(r.inner, name) }
func (r roFS) ReadLink(name string) (string, error)       { return fs.ReadLink(r.inner, name) }

// roFile exposes the read side of an *os.File only.
type roFile struct{ f *os.File }

func (r roFile) Read(p []byte) (int, error)                { return r.f.Read(p) }
func (r roFile) ReadAt(p []byte, off int64) (int, error)   { return r.f.ReadAt(p, off) }
func (r roFile) Seek(off int64, whence int) (int64, error) { return r.f.Seek(off, whence) }
func (r roFile) Stat() (fs.FileInfo, error)                { return r.f.Stat() }
func (r roFile) Close() error                              { return r.f.Close() }
func (r roFile) ReadDir(n int) ([]fs.DirEntry, error)      { return r.f.ReadDir(n) }

var _ io.ReadSeeker = roFile{}
var _ io.ReaderAt = roFile{}

package inbox

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func idOf(t *testing.T, dir, rel string) FileID {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	id, ok := IDOf(fi)
	if !ok {
		t.Fatal("no dev/ino")
	}
	return id
}

func TestInbox_ReadOnlyHasNoWriteMethods(t *testing.T) {
	typ := reflect.TypeOf(&ReadOnly{})
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	if want := []string{"FS", "OpenRegular"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("method set = %v, want %v", got, want)
	}

	dir := t.TempDir()
	write(t, dir, "a.txt", "hello")
	ro, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fsys := ro.FS()
	banned := []string{"Write", "WriteFile", "Create", "Remove", "RemoveAll", "Mkdir", "MkdirAll", "Rename", "OpenFile", "Symlink", "Link", "Chmod", "Chtimes", "Truncate"}
	ft := reflect.TypeOf(fsys)
	for _, b := range banned {
		if _, ok := ft.MethodByName(b); ok {
			t.Errorf("FS() value has method %s", b)
		}
	}
	f, err := fsys.Open("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, ok := f.(io.Writer); ok {
		t.Error("file from FS().Open is writable")
	}
	if _, ok := f.(io.WriterAt); ok {
		t.Error("file from FS().Open is WriterAt")
	}
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read = %q, %v", b, err)
	}
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil || len(ents) != 1 {
		t.Fatalf("ReadDir = %v, %v", ents, err)
	}
	if _, err := fs.Stat(fsys, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Lstat(fsys, "a.txt"); err != nil {
		t.Fatal(err)
	}
	// Escapes are refused by the root.
	if _, err := fsys.Open("../x"); err == nil {
		t.Error("open outside root succeeded")
	}
}

func TestInbox_OpenMissingDir(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error")
	}
}

func TestInbox_OpenRegular(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "d/a.bin", "abcdef")
	ro, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := idOf(t, dir, "d/a.bin")
	f, err := ro.OpenRegular("d/a.bin", want)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, ok := f.(io.Writer); ok {
		t.Error("OpenRegular file is writable")
	}
	ra, ok := f.(io.ReaderAt)
	if !ok {
		t.Fatal("file is not an io.ReaderAt")
	}
	buf := make([]byte, 3)
	if _, err := ra.ReadAt(buf, 2); err != nil || string(buf) != "cde" {
		t.Fatalf("ReadAt = %q, %v", buf, err)
	}
	sk, ok := f.(io.Seeker)
	if !ok {
		t.Fatal("file is not a Seeker")
	}
	if _, err := sk.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(f)
	if string(all) != "abcdef" {
		t.Fatalf("read = %q", all)
	}
	if _, err := f.Stat(); err != nil {
		t.Fatal(err)
	}

	// size change => ErrChanged
	changed := want
	changed.Size++
	if _, err := ro.OpenRegular("d/a.bin", changed); !errors.Is(err, ErrChanged) {
		t.Fatalf("size change: %v", err)
	}
	changed = want
	changed.ModTime = want.ModTime.Add(time.Hour)
	if _, err := ro.OpenRegular("d/a.bin", changed); !errors.Is(err, ErrChanged) {
		t.Fatalf("mtime change: %v", err)
	}
	changed = want
	changed.Ino++
	if _, err := ro.OpenRegular("d/a.bin", changed); !errors.Is(err, ErrChanged) {
		t.Fatalf("ino change: %v", err)
	}
	if _, err := ro.OpenRegular("missing", want); err == nil {
		t.Fatal("missing file opened")
	}
	if _, err := ro.OpenRegular("d", want); !errors.Is(err, ErrChanged) {
		t.Fatalf("directory: %v", err)
	}
}

func TestInbox_OpenRegularRejectsSwaps(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.flac", "data")
	write(t, dir, "y.flac", "data")
	ro, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantX := idOf(t, dir, "x.flac")
	wantY := idOf(t, dir, "y.flac")

	// replaced by a FIFO
	if err := os.Remove(filepath.Join(dir, "x.flac")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "x.flac"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// replaced by a symlink
	if err := os.Remove(filepath.Join(dir, "y.flac")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("x.flac", filepath.Join(dir, "y.flac")); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		want FileID
	}{{"x.flac", wantX}, {"y.flac", wantY}} {
		done := make(chan error, 1)
		go func() {
			f, err := ro.OpenRegular(c.name, c.want)
			if err == nil {
				f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrChanged) {
				t.Errorf("%s: err = %v, want ErrChanged", c.name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: OpenRegular blocked", c.name)
		}
	}
}

func TestInbox_FSReadOnlyOperations(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "d/a.txt", "x")
	if err := os.Symlink("a.txt", filepath.Join(dir, "d", "ln")); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fsys := ro.FS()
	if tgt, err := fs.ReadLink(fsys, "d/ln"); err != nil || tgt != "a.txt" {
		t.Fatalf("ReadLink = %q %v", tgt, err)
	}
	fi, err := fs.Lstat(fsys, "d/ln")
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat = %v %v", fi, err)
	}
	f, err := fsys.Open("d")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		t.Fatal("directory handle has no ReadDir")
	}
	ents, err := rd.ReadDir(-1)
	if err != nil || len(ents) != 2 {
		t.Fatalf("ReadDir = %v %v", ents, err)
	}
	if _, err := fsys.Open("missing"); err == nil {
		t.Error("missing file opened")
	}
	// Open of a symlink through the guard is refused as a swap
	if _, err := ro.OpenRegular("d/ln", FileID{}); !errors.Is(err, ErrChanged) {
		t.Errorf("symlink OpenRegular = %v", err)
	}
}

func TestInbox_OpenRegularPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores modes")
	}
	dir := t.TempDir()
	write(t, dir, "locked", "x")
	want := idOf(t, dir, "locked")
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	ro, _ := Open(dir)
	_, err := ro.OpenRegular("locked", want)
	if err == nil || errors.Is(err, ErrChanged) {
		t.Errorf("err = %v, want a permission error", err)
	}
}

func TestIDOf_NoSysStat(t *testing.T) {
	if _, ok := IDOf(fakeInfo{}); ok {
		t.Error("IDOf accepted a FileInfo without dev/ino")
	}
}

type fakeInfo struct{ fs.FileInfo }

func (fakeInfo) Sys() any { return nil }

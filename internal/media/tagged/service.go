// Package tagged writes downloadable FLAC copies carrying the current tags (C110.2, D-M4a-14): STREAMINFO and
// SEEKTABLE copied verbatim, a new VORBIS_COMMENT and PICTURE, then the master's audio frames byte for byte,
// cached input-addressed under derived/dl/. The master is never modified (rule 7).
package tagged

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

const (
	dlDir      = "derived/dl"
	coverSize  = 512
	artist     = "metamusic"
	fallbackDL = "download.flac"
)

func relPath(key string) string { return dlDir + "/" + key[:2] + "/" + key + ".flac" }

func isKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Download is one servable tagged copy (or, for a lossy master, the master itself, D-M4a-25).
type Download struct {
	Object   serve.Object
	Key      string
	Filename string
	Bytes    int64
	SHA256   string // of the served file
	Tags     Tags
}

// Service builds and caches tagged copies.
type Service struct {
	data   *os.Root
	lib    *library.Service
	covers *cover.Service
	now    func() time.Time
	shas   sync.Map // key -> sha256 of the cached file (a pure function of the key, so never stale)
}

// New builds the download service; a nil now means time.Now.
func New(data *os.Root, lib *library.Service, covers *cover.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{data: data, lib: lib, covers: covers, now: now}
}

func (s *Service) ref(ctx context.Context, trackID string) (library.TrackRef, error) {
	refs, err := s.lib.TrackRefs(ctx, []string{trackID})
	if err != nil {
		return library.TrackRef{}, err
	}
	r, ok := refs[trackID]
	if !ok {
		return r, library.ErrNotFound
	}
	return r, nil
}

func filename(title string, seed int64, ext string) string {
	slug := library.Slug(title)
	if slug == "" {
		slug = "track"
	}
	return slug + "__" + strconv.FormatInt(seed, 10) + ext
}

// openMaster opens the master read-only; a missing file is library.ErrMediaUnavailable.
func (s *Service) openMaster(ref library.TrackRef) (*os.File, fs.FileInfo, error) {
	f, err := s.data.Open(ref.MasterPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, library.ErrMediaUnavailable
		}
		return nil, nil, fmt.Errorf("tagged: open master: %w", err)
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, library.ErrMediaUnavailable
	}
	return f, st, nil
}

// tags assembles the current tags of a track; info is the master's parsed metadata (its ReplayGain tags are
// copied), nil for none.
func (s *Service) tags(ctx context.Context, ref library.TrackRef, info *flacmeta.Info) (Tags, error) {
	detail, err := s.lib.Track(ctx, ref.ID)
	if err != nil {
		return Tags{}, err
	}
	lineage, err := s.lib.LineageTracks(ctx, ref.RootTrackID)
	if err != nil {
		return Tags{}, err
	}
	t := Tags{Title: ref.Title, Artist: artist, Album: ref.Title, TrackNumber: 1, TrackTotal: len(lineage),
		Date: ref.RecordedAt.UTC().Format("2006-01-02")}
	for i, l := range lineage {
		if l.ID == ref.ID {
			t.TrackNumber = i + 1
		}
		if l.ID == ref.RootTrackID {
			t.Album = l.Title
		}
	}
	if len(detail.StyleTerms) > 0 {
		t.Genre = detail.StyleTerms[0]
	}
	if info != nil {
		for _, c := range info.Comments {
			k, v, _ := strings.Cut(c, "=")
			switch strings.ToUpper(k) {
			case "REPLAYGAIN_TRACK_GAIN":
				t.ReplayGainTrackGain = v
			case "REPLAYGAIN_TRACK_PEAK":
				t.ReplayGainTrackPeak = v
			}
		}
	}
	seed := [2]string{"MM_SEED", strconv.FormatInt(ref.Seed, 10)}
	if !ref.RecipeKnown {
		seed = [2]string{"MM_REFERENCE", "1"}
	}
	t.MM = [][2]string{{"MM_TAKE_ID", ref.ID}, {"MM_RECIPE_ID", detail.Recipe.ID}, {"MM_REQUEST_ID", ref.RequestID}, seed, {"MM_STATUS", ref.Status}}
	return t, nil
}

// TagsOf returns the tags a download of the track carries (for a lossy track: the tags its metadata would have).
// An unknown track is library.ErrNotFound.
func (s *Service) TagsOf(ctx context.Context, trackID string) (Tags, error) {
	ref, err := s.ref(ctx, trackID)
	if err != nil {
		return Tags{}, err
	}
	if ref.Lossy {
		return s.tags(ctx, ref, nil)
	}
	f, st, err := s.openMaster(ref)
	if err != nil {
		return Tags{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		return Tags{}, fmt.Errorf("tagged: read master: %w", err)
	}
	return s.tags(ctx, ref, &info)
}

func tagsKey(masterSHA string, t Tags, coverKey string) string {
	canon, _ := json.Marshal(t) // a struct of strings, ints and string pairs cannot fail
	h := sha256.New()
	for _, part := range [][]byte{[]byte(Version), []byte(masterSHA), canon, []byte(coverKey)} {
		h.Write(part)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Ensure returns the tagged copy of a track, writing it on first use. A lossy master is returned as it is
// (D-M4a-25): nothing is written. An unknown track is library.ErrNotFound, a missing master
// library.ErrMediaUnavailable.
func (s *Service) Ensure(ctx context.Context, trackID string) (Download, error) {
	ref, err := s.ref(ctx, trackID)
	if err != nil {
		return Download{}, err
	}
	f, mst, err := s.openMaster(ref)
	if err != nil {
		return Download{}, err
	}
	defer func() { _ = f.Close() }()
	size := mst.Size()
	if ref.Lossy {
		return Download{
			Object: serve.Object{RelPath: ref.MasterPath, ContentType: "audio/mpeg", ETag: ref.MasterSHA256, ModTime: mst.ModTime()},
			Key:    ref.MasterSHA256, Filename: filename(ref.Title, ref.Seed, ".mp3"), Bytes: size, SHA256: ref.MasterSHA256,
		}, nil
	}
	info, err := flacmeta.Read(f, size)
	if err != nil {
		return Download{}, fmt.Errorf("tagged: read master: %w", err)
	}
	tags, err := s.tags(ctx, ref, &info)
	if err != nil {
		return Download{}, err
	}
	cobj, coverKey, err := s.covers.EnsureTrack(ctx, trackID, coverSize)
	if err != nil {
		return Download{}, err
	}
	coverPNG, err := s.data.ReadFile(cobj.RelPath)
	if err != nil {
		return Download{}, fmt.Errorf("tagged: read cover: %w", err)
	}
	key := tagsKey(ref.MasterSHA256, tags, coverKey)
	d := Download{Key: key, Filename: filename(ref.Title, ref.Seed, ".flac"), Tags: tags}
	rel := relPath(key)
	if st, err := s.data.Lstat(rel); err == nil && st.Mode().IsRegular() {
		sha, ok := s.shas.Load(key)
		if !ok {
			if sha, err = s.hashFile(rel); err != nil {
				return Download{}, err
			}
			s.shas.Store(key, sha)
		}
		d.Object = serve.Object{RelPath: rel, ContentType: "audio/flac", ETag: key, ModTime: st.ModTime()}
		d.Bytes, d.SHA256 = st.Size(), sha.(string)
		return d, nil
	}
	sha, err := s.build(f, size, info, tags, coverPNG, key)
	if err != nil {
		return Download{}, err
	}
	s.shas.Store(key, sha)
	st, err := s.data.Lstat(rel)
	if err != nil {
		return Download{}, fmt.Errorf("tagged: stat download: %w", err)
	}
	d.Object = serve.Object{RelPath: rel, ContentType: "audio/flac", ETag: key, ModTime: st.ModTime()}
	d.Bytes, d.SHA256 = st.Size(), sha
	return d, nil
}

func (s *Service) hashFile(rel string) (string, error) {
	f, err := s.data.Open(rel)
	if err != nil {
		return "", fmt.Errorf("tagged: open download: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("tagged: hash download: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// build writes the copy into incoming/dl-<uuid>/ and claims derived/dl/<aa>/<key>.flac with Root.Link (EEXIST
// means a concurrent call stored the identical content). It returns the sha256 of the file.
func (s *Service) build(master io.ReaderAt, size int64, info flacmeta.Info, tags Tags, coverPNG []byte, key string) (string, error) {
	if err := s.data.MkdirAll("incoming", 0o755); err != nil {
		return "", fmt.Errorf("tagged: create incoming: %w", err)
	}
	work := "incoming/dl-" + uuid.NewV7().String()
	if err := s.data.Mkdir(work, 0o755); err != nil {
		return "", fmt.Errorf("tagged: create work dir: %w", err)
	}
	defer func() { _ = s.data.RemoveAll(work) }()
	tmp := work + "/download.flac"
	out, err := s.data.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return "", fmt.Errorf("tagged: create copy: %w", err)
	}
	h := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(out, h), 1<<20)
	_, err = Write(bw, master, size, info, tags, coverPNG)
	if err == nil {
		err = bw.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	// the sweep ages a copy by its mtime, which is the creation time on the service clock
	if err := s.data.Chtimes(tmp, s.now(), s.now()); err != nil {
		return "", fmt.Errorf("tagged: stamp: %w", err)
	}
	dst := relPath(key)
	if err := s.data.MkdirAll(path.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("tagged: create cache dir: %w", err)
	}
	if err := s.data.Link(tmp, dst); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("tagged: link into place: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Resolve implements serve.Resolver for ext "flac", files under derived/dl/ only. The ETag is the key (the file is
// a pure function of it). DownloadName comes from the file's own TITLE and MM_SEED tags.
func (s *Service) Resolve(_ context.Context, key, ext string) (serve.Object, error) {
	if ext != "flac" || !isKey(key) {
		return serve.Object{}, serve.ErrNotFound
	}
	rel := relPath(key)
	st, err := s.data.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("tagged: resolve: %w", err)
	case !st.Mode().IsRegular():
		return serve.Object{}, serve.ErrNotFound
	}
	return serve.Object{RelPath: rel, ContentType: "audio/flac", ETag: key, ModTime: st.ModTime(), DownloadName: s.nameOf(rel, st.Size())}, nil
}

// nameOf derives the download name from the file's tags; a file whose tags cannot be read gets a generic name.
func (s *Service) nameOf(rel string, size int64) string {
	f, err := s.data.Open(rel)
	if err != nil {
		return fallbackDL
	}
	defer func() { _ = f.Close() }()
	info, err := flacmeta.Read(f, size)
	if err != nil {
		return fallbackDL
	}
	title, seed := "", int64(0)
	for _, c := range info.Comments {
		k, v, _ := strings.Cut(c, "=")
		switch k {
		case "TITLE":
			title = v
		case "MM_SEED":
			seed, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return filename(title, seed, ".flac")
}

// Sweep removes download files whose modification time is older than olderThan and returns how many. The next
// request recreates a swept copy under the same key.
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := s.now().Add(-olderThan)
	var stale []string
	err := fs.WalkDir(s.data.FS(), dlDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".flac") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			stale = append(stale, p)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("tagged: sweep: %w", err)
	}
	n := 0
	for _, p := range stale {
		switch err := s.data.Remove(p); {
		case err == nil:
			n++
			if base := strings.TrimSuffix(path.Base(p), ".flac"); isKey(base) {
				s.shas.Delete(base)
			}
			_ = s.data.Remove(path.Dir(p)) // the prefix directory, only when now empty
		case errors.Is(err, fs.ErrNotExist):
		default:
			return n, fmt.Errorf("tagged: sweep %s: %w", p, err)
		}
	}
	return n, nil
}

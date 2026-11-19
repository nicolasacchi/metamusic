// Package clean builds the two audio renditions a Listener may receive (D-U1-17, C308.7): mp3_320_clean and
// flac_clean. Both are derived from a frozen master by copying audio bytes verbatim (no re-encode, so the audio of
// flac_clean is bit-identical to the master's) and carry one metadata item, the title. Everything else the master
// holds (MM_SEED, MM_REQUEST_ID, MM_RECIPE_ID, MM_TAKE_ID, MM_STATUS, pictures, comments, ReplayGain, any other tag)
// is left behind: the build is an allow-list, and the output is re-parsed and refused unless it passes the same
// allow-list that `library verify` applies. Nothing is prebuilt: the first Listener request builds the file, later
// requests find its row. The master is only ever read (rule 7).
package clean

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/id3"
	"github.com/nicolasacchi/metamusic/internal/media/mp3info"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The two rendition kinds (rows of the renditions table, keyed (track_id, kind); the kind is validated here, in Go).
const (
	KindMP3  = "mp3_320_clean"
	KindFLAC = "flac_clean"
)

// Version is hashed into every row's fingerprint: bumping it rebuilds every rendition on the next request.
const Version = "mm-clean/1"

// Vendor is the Vorbis vendor string of a built FLAC; verify refuses any other.
const Vendor = "metamusic " + Version

const (
	maxTitleBytes = 1024
	defaultCap    = 2
	defaultQueue  = 8
	retryAfter    = 5 * time.Second
	buildTimeout  = 5 * time.Minute
	workDir       = "incoming"
)

// IsKind reports whether kind is one of the two clean kinds.
func IsKind(kind string) bool { return kind == KindMP3 || kind == KindFLAC }

// ErrBusy means every build slot is taken and the wait queue is full (503 RENDITION_BUSY).
type ErrBusy struct{ RetryAfter time.Duration }

func (e *ErrBusy) Error() string { return "clean: rendition builds are busy" }

// ErrUnavailable means the rendition could not be built or did not pass the allow-list check. Nothing was cached
// and nothing is served: the caller fails closed (a Listener never falls back to the master).
type ErrUnavailable struct{ Err error }

func (e *ErrUnavailable) Error() string { return "clean: rendition unavailable: " + e.Err.Error() }
func (e *ErrUnavailable) Unwrap() error { return e.Err }

// Rendition is one servable clean file.
type Rendition struct {
	Kind        string
	Ext         string // "mp3" or "flac"
	ContentType string
	SHA256      string // of the rendition file; the only hash a Listener ever sees
	Bytes       int64
	Path        string // DataDir-relative
	Filename    string // slug of the title plus the extension; never the seed
	Title       string
	Lossy       bool // the track's master is an MP3, so flac was requested but the MP3 answers
	ModTime     time.Time
}

// Object is the servable form of the rendition.
func (r Rendition) Object() serve.Object {
	return serve.Object{RelPath: r.Path, ContentType: r.ContentType, ETag: r.SHA256, ModTime: r.ModTime}
}

// Options are the collaborators of New.
type Options struct {
	Store *store.Store
	Data  *os.Root
	Now   func() time.Time
	Cap   int // concurrent builds, both kinds together (default 2)
	Queue int // builds allowed to wait for a slot (0: default 8; negative: none); beyond it ErrBusy
}

// Service builds and caches clean renditions.
type Service struct {
	st    *store.Store
	data  *os.Root
	now   func() time.Time
	slots chan struct{}
	queue int64
	wait  atomic.Int64

	mu     sync.Mutex
	flight map[string]*call

	builds atomic.Int64 // completed builds (tests assert the cache by it)
	linkMu sync.Mutex
	hold   func() // tests: runs inside a build slot before the build
}

type call struct {
	done chan struct{}
	r    Rendition
	err  error
}

// New builds the service. A nil Store or Data makes every Ensure fail closed with ErrUnavailable.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Cap <= 0 {
		o.Cap = defaultCap
	}
	switch {
	case o.Queue == 0:
		o.Queue = defaultQueue
	case o.Queue < 0:
		o.Queue = 0
	}
	return &Service{st: o.Store, data: o.Data, now: o.Now, slots: make(chan struct{}, o.Cap), queue: int64(o.Queue), flight: map[string]*call{}}
}

// Builds is the number of builds completed since New.
func (s *Service) Builds() int64 { return s.builds.Load() }

type trackRow struct {
	ID, Title, MasterPath, MasterSHA string
	Lossy                            bool
}

func (s *Service) track(ctx context.Context, id string) (trackRow, error) {
	t := trackRow{ID: id}
	var lossy int
	err := s.st.R.QueryRowContext(ctx, `SELECT title, master_path, master_sha256, lossy FROM tracks WHERE id = ?`, id).Scan(&t.Title, &t.MasterPath, &t.MasterSHA, &lossy)
	if errors.Is(err, sql.ErrNoRows) {
		return t, library.ErrNotFound
	}
	t.Lossy = lossy == 1
	return t, err
}

type row struct {
	sha, path, params string
	bytes             int64
	created           string
}

func (s *Service) row(ctx context.Context, id, kind string) (row, bool, error) {
	var r row
	err := s.st.R.QueryRowContext(ctx, `SELECT sha256, bytes, path, params_json, created_at FROM renditions WHERE track_id = ? AND kind = ?`, id, kind).
		Scan(&r.sha, &r.bytes, &r.path, &r.params, &r.created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

type params struct {
	V  string `json:"v"`
	FP string `json:"fp"`
}

// fingerprint is a pure function of what the file is built from: the title (an Editor may change it) and the source
// file (the master, or for a lossless track the mp3_320 rendition), so a change of either rebuilds on the next request.
func fingerprint(kind, title, srcSHA string) string {
	h := sha256.New()
	for _, p := range []string{Version, kind, title, srcSHA} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Ensure returns the clean rendition of the track for the wanted kind, building it on first use. A lossy master has
// no lossless file: either kind answers mp3_320_clean (Rendition.Lossy). The track's visibility is the caller's
// (library.Visible); an unknown track is library.ErrNotFound, a missing source file library.ErrMediaUnavailable.
func (s *Service) Ensure(ctx context.Context, trackID, kind string) (Rendition, error) {
	if !IsKind(kind) {
		return Rendition{}, &ErrUnavailable{Err: fmt.Errorf("unknown kind %q", kind)}
	}
	if s == nil || s.st == nil || s.data == nil {
		return Rendition{}, &ErrUnavailable{Err: errors.New("renditions are not configured")}
	}
	t, err := s.track(ctx, trackID)
	if err != nil {
		return Rendition{}, err
	}
	lossy := t.Lossy
	if lossy {
		kind = KindMP3
	}
	src, err := s.source(ctx, t, kind)
	if err != nil {
		return Rendition{}, err
	}
	fp := fingerprint(kind, t.Title, src.sha)
	if r, ok := s.cached(ctx, t, kind, fp); ok {
		r.Lossy = lossy
		return r, nil
	}
	key := trackID + "|" + kind
	for {
		s.mu.Lock()
		c, joined := s.flight[key]
		if !joined {
			c = &call{done: make(chan struct{})}
			s.flight[key] = c
		}
		s.mu.Unlock()
		if !joined {
			c.r, c.err = s.buildSlot(ctx, t, kind, fp, src)
			s.mu.Lock()
			delete(s.flight, key)
			s.mu.Unlock()
			close(c.done)
			r := c.r
			r.Lossy = lossy
			return r, c.err
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			return Rendition{}, ctx.Err()
		}
		// the leader's own context ended while it waited for a slot: that is not this caller's failure; lead anew
		if (errors.Is(c.err, context.Canceled) || errors.Is(c.err, context.DeadlineExceeded)) && ctx.Err() == nil {
			continue
		}
		r := c.r
		r.Lossy = lossy
		return r, c.err
	}
}

// source is the file a rendition is copied from: the frozen master for flac_clean and for a lossy master, the
// mp3_320 rendition (which already carries the loudness gain) for the MP3 of a lossless track.
type source struct{ path, sha string }

func (s *Service) source(ctx context.Context, t trackRow, kind string) (source, error) {
	if kind == KindFLAC || t.Lossy {
		return source{t.MasterPath, t.MasterSHA}, nil
	}
	r, ok, err := s.row(ctx, t.ID, "mp3_320")
	if err != nil {
		return source{}, err
	}
	if !ok {
		return source{}, library.ErrMediaUnavailable
	}
	return source{r.path, r.sha}, nil
}

// cached returns the stored rendition when its row matches the fingerprint and the file is still there.
func (s *Service) cached(ctx context.Context, t trackRow, kind, fp string) (Rendition, bool) {
	r, ok, err := s.row(ctx, t.ID, kind)
	if err != nil || !ok {
		return Rendition{}, false
	}
	var p params
	if json.Unmarshal([]byte(r.params), &p) != nil || p.FP != fp {
		return Rendition{}, false
	}
	st, err := s.data.Lstat(r.path)
	if err != nil || !st.Mode().IsRegular() || st.Size() != r.bytes {
		return Rendition{}, false
	}
	return s.rendition(t, kind, r), true
}

func (s *Service) rendition(t trackRow, kind string, r row) Rendition {
	ext, ct := "flac", "audio/flac"
	if kind == KindMP3 {
		ext, ct = "mp3", "audio/mpeg"
	}
	slug := library.Slug(t.Title)
	if slug == "" {
		slug = "track"
	}
	mt, _ := time.Parse(time.RFC3339, r.created)
	return Rendition{Kind: kind, Ext: ext, ContentType: ct, SHA256: r.sha, Bytes: r.bytes, Path: r.path, Filename: slug + "." + ext, Title: t.Title, ModTime: mt}
}

// buildSlot waits for a build slot (bounded queue), then builds. Any failure of the build itself is an
// ErrUnavailable: the caller never serves the master instead.
func (s *Service) buildSlot(ctx context.Context, t trackRow, kind, fp string, src source) (Rendition, error) {
	select {
	case s.slots <- struct{}{}:
	default:
		if s.wait.Add(1) > s.queue {
			s.wait.Add(-1)
			return Rendition{}, &ErrBusy{RetryAfter: retryAfter}
		}
		select {
		case s.slots <- struct{}{}:
			s.wait.Add(-1)
		case <-ctx.Done():
			s.wait.Add(-1)
			return Rendition{}, ctx.Err()
		}
	}
	defer func() { <-s.slots }()
	if s.hold != nil {
		s.hold()
	}
	// the build outlives a leader whose client went away: joiners and the cache profit from it
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
	defer cancel()
	r, err := s.build(bctx, t, kind, fp, src)
	if err != nil && !errors.Is(err, library.ErrMediaUnavailable) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		err = &ErrUnavailable{Err: err}
	}
	return r, err
}

func (s *Service) build(ctx context.Context, t trackRow, kind, fp string, src source) (Rendition, error) {
	in, err := s.data.Open(src.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Rendition{}, library.ErrMediaUnavailable
		}
		return Rendition{}, err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return Rendition{}, library.ErrMediaUnavailable
	}
	if err := s.data.MkdirAll(workDir, 0o755); err != nil {
		return Rendition{}, err
	}
	work := workDir + "/clean-" + uuid.NewV7().String()
	if err := s.data.Mkdir(work, 0o755); err != nil {
		return Rendition{}, err
	}
	defer func() { _ = s.data.RemoveAll(work) }()
	ext := "flac"
	if kind == KindMP3 {
		ext = "mp3"
	}
	tmp := work + "/out." + ext
	out, err := s.data.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return Rendition{}, err
	}
	h := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(out, h), 1<<20)
	title := sanitize(t.Title)
	if kind == KindFLAC {
		err = writeFLAC(bw, in, st.Size(), title)
	} else {
		err = writeMP3(bw, in, st.Size(), title)
	}
	if err == nil {
		err = bw.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Rendition{}, err
	}
	if err := ctx.Err(); err != nil {
		return Rendition{}, err
	}
	chk, err := s.data.Open(tmp)
	if err != nil {
		return Rendition{}, err
	}
	cst, err := chk.Stat()
	if err == nil {
		err = Check(kind, chk, cst.Size())
	}
	_ = chk.Close()
	if err != nil {
		return Rendition{}, fmt.Errorf("allow-list check of the built file: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	rel := "derived/" + sum[:2] + "/" + sum + "." + ext
	if err := s.data.MkdirAll("derived/"+sum[:2], 0o755); err != nil {
		return Rendition{}, err
	}
	// link, row and dropOld are one step: a twin build must not see its linked file unreferenced and remove it
	s.linkMu.Lock()
	defer s.linkMu.Unlock()
	if err := s.data.Link(tmp, rel); err != nil && !errors.Is(err, fs.ErrExist) {
		return Rendition{}, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	pj, _ := json.Marshal(params{V: Version, FP: fp})
	var old string
	_ = s.st.R.QueryRowContext(ctx, `SELECT path FROM renditions WHERE track_id = ? AND kind = ?`, t.ID, kind).Scan(&old)
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(track_id, kind) DO UPDATE SET sha256 = excluded.sha256, bytes = excluded.bytes, path = excluded.path,
		params_json = excluded.params_json, created_at = excluded.created_at`, t.ID, kind, sum, cst.Size(), rel, string(pj), now); err != nil {
		return Rendition{}, fmt.Errorf("record rendition: %w", err)
	}
	s.dropOld(ctx, old, rel)
	s.builds.Add(1)
	return s.rendition(t, kind, row{sha: sum, bytes: cst.Size(), path: rel, created: now}), nil
}

// dropOld removes the file a rebuild replaced, once no row names it (two tracks of identical audio and title share one).
func (s *Service) dropOld(ctx context.Context, old, now string) {
	if old == "" || old == now || !strings.HasPrefix(old, "derived/") {
		return
	}
	var n int
	if err := s.st.W.QueryRowContext(ctx, `SELECT COUNT(*) FROM renditions WHERE path = ?`, old).Scan(&n); err == nil && n == 0 {
		_ = s.data.Remove(old)
	}
}

// Resolve implements serve.Resolver for the rendition files, addressed by their own sha256. A row whose file is gone
// answers ErrNotFound (verify removed it, or it is awaiting a rebuild).
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	if s == nil || s.st == nil || s.data == nil || (ext != "mp3" && ext != "flac") {
		return serve.Object{}, serve.ErrNotFound
	}
	kind, ct := KindFLAC, "audio/flac"
	if ext == "mp3" {
		kind, ct = KindMP3, "audio/mpeg"
	}
	var path, created string
	err := s.st.R.QueryRowContext(ctx, `SELECT path, created_at FROM renditions WHERE kind = ? AND sha256 = ? LIMIT 1`, kind, sha).Scan(&path, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return serve.Object{}, serve.ErrNotFound
	}
	if err != nil {
		return serve.Object{}, err
	}
	if st, err := s.data.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return serve.Object{}, serve.ErrNotFound
	}
	mt, _ := time.Parse(time.RFC3339, created)
	return serve.Object{RelPath: path, ContentType: ct, ETag: sha, ModTime: mt}, nil
}

// sanitize makes the title safe for a tag: invalid UTF-8 becomes U+FFFD, controls become a space, the result is
// trimmed and capped at 1024 bytes on a rune boundary.
func sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch {
		case r == utf8.RuneError && w == 1:
			r = '�'
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			r = ' '
		}
		if b.Len()+utf8.RuneLen(r) > maxTitleBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// writeFLAC copies STREAMINFO, the first SEEKTABLE and the audio frames of the master, and replaces every other block
// by one VORBIS_COMMENT holding TITLE: PICTURE, CUESHEET, APPLICATION, PADDING and the master's comments are dropped.
func writeFLAC(dst io.Writer, master io.ReaderAt, size int64, title string) error {
	info, err := flacmeta.Read(master, size)
	if err != nil {
		return fmt.Errorf("read master: %w", err)
	}
	vc := flacmeta.VorbisComment(Vendor, []flacmeta.Comment{{Key: "TITLE", Value: title}})
	_, err = flacmeta.Rewrite(dst, master, size, info, flacmeta.Replace{Vorbis: vc, Cuesheet: []byte{}, Picture: []byte{}})
	return err
}

// writeMP3 writes a lone TIT2 tag followed by the MPEG frames of src: leading tags and trailers are not copied.
func writeMP3(dst io.Writer, src io.ReaderAt, size int64, title string) error {
	start, end, err := mp3info.Span(src, size)
	if err != nil {
		return fmt.Errorf("read mp3 source: %w", err)
	}
	if _, err := dst.Write(id3.EncodeTitle(title)); err != nil {
		return err
	}
	if err := checkTail(src, end, size); err != nil {
		return err
	}
	n, err := io.Copy(dst, io.NewSectionReader(src, start, end-start))
	if err == nil && n != end-start {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// maxFrame is the largest MPEG audio frame (MPEG-1 layer III at 320 kb/s is 1045 bytes; this leaves slack).
const maxFrame = 2048

// checkTail fails closed when the frame span stops before the end of the source for any reason other than a known
// trailer (ID3v1, APE, Lyrics3) or a cut last frame: a damaged or mid-stream-tagged source would otherwise be served
// shortened and pass the allow-list.
func checkTail(src io.ReaderAt, end, size int64) error {
	rest := size - end
	if rest <= 0 || rest < maxFrame {
		return nil
	}
	var b [16]byte
	n, _ := src.ReadAt(b[:], end)
	head := b[:n]
	for _, p := range []string{"TAG", "APETAGEX", "LYRICSBEGIN"} {
		if bytes.HasPrefix(head, []byte(p)) {
			return nil
		}
	}
	return fmt.Errorf("mp3 source has %d bytes after its last frame that are not a known trailer", rest)
}

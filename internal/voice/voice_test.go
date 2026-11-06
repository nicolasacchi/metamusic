package voice_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/voice"
)

func at(f float64) *float64 { return &f }

func voiceCode(t *testing.T, err error, status int, code, reason string) {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
	if reason != "" && pe.Details["reason"] != reason {
		t.Fatalf("reason = %v, want %s", pe.Details["reason"], reason)
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	pad := func(b ...byte) []byte { return append(b, make([]byte, 64)...) }
	for name, tc := range map[string]struct {
		in        []byte
		cont, cdc string
		ok        bool
	}{
		"webm opus":   {append(pad(0x1A, 0x45, 0xDF, 0xA3), []byte("A_OPUS")...), "webm", "opus", true},
		"webm vorbis": {append(pad(0x1A, 0x45, 0xDF, 0xA3), []byte("A_VORBIS")...), "", "", false},
		"ogg opus":    {append(pad([]byte("OggS")...), []byte("OpusHead")...), "ogg", "opus", true},
		"ogg vorbis":  {append(pad([]byte("OggS")...), []byte("\x01vorbis")...), "ogg", "vorbis", true},
		"ogg flac":    {append(pad([]byte("OggS")...), []byte("\x7fFLAC")...), "", "", false},
		"mp4 aac":     {append([]byte("\x00\x00\x00\x18ftypisom"), []byte("....mp4a....")...), "mp4", "aac", true},
		"mp4 opus":    {append([]byte("\x00\x00\x00\x18ftypiso5"), []byte("....Opus....")...), "mp4", "opus", true},
		"mp4 unknown": {[]byte("\x00\x00\x00\x18ftypisom........alac...."), "", "", false},
		"wav":         {[]byte("RIFF\x24\x00\x00\x00WAVEfmt "), "", "", false},
		"short":       {[]byte("ftyp"), "", "", false},
		"empty":       {nil, "", "", false},
	} {
		c, k, ok := voice.Detect(tc.in)
		if c != tc.cont || k != tc.cdc || ok != tc.ok {
			t.Errorf("%s: got %q %q %v", name, c, k, ok)
		}
	}
}

// TestVoice_ContainerMatrix: every container the browsers' MediaRecorder produces (webm/opus, ogg/opus, iOS MP4 with aac or
// opus) plus ogg/vorbis is stored content-addressed and frozen, with a playable MP3 rendition and its comment.
func TestVoice_ContainerMatrix(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	e := newEnv(t, "")
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			ctx := auth.WithPrincipal(context.Background(), human)
			id, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b64(fx.data), AtSecond: at(1.04), Kind: "fix"})
			if err != nil {
				t.Fatal(err)
			}
			c, err := e.judg.CommentByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if c.Kind != "fix" || c.State != "open" || c.Source != "web" || c.AtSecond == nil || *c.AtSecond != 1.0 {
				t.Fatalf("comment %+v", c)
			}
			if c.Body != "[voice note 0:02]" {
				t.Errorf("default body %q", c.Body)
			}
			v := c.Voice
			if v == nil || v.Container != fx.container || v.Codec != fx.codec || v.Bytes != int64(len(fx.data)) || v.DurationS < 1.8 || v.DurationS > 2.3 {
				t.Fatalf("voice %+v", v)
			}
			if !strings.HasPrefix(v.URL, "/media/") || !strings.Contains(v.URL, ".mp3?exp=") {
				t.Errorf("url %q is not a signed mp3 link", v.URL)
			}
			var path, mp3path, sha string
			var mp3bytes int64
			if err := e.st.R.QueryRow(`SELECT path, mp3_path, sha256, mp3_bytes FROM voice_notes WHERE id = ?`, v.ID).Scan(&path, &mp3path, &sha, &mp3bytes); err != nil {
				t.Fatal(err)
			}
			if want := "voice/" + sha[:2] + "/" + sha + "." + fx.container; path != want {
				t.Errorf("path %q, want %q", path, want)
			}
			for _, rel := range []string{path, mp3path} {
				st, err := e.root.Stat(rel)
				if err != nil || st.Mode().Perm() != 0o444 {
					t.Errorf("%s: %v %v, want mode 0444", rel, st, err)
				}
			}
			// the rendition is a real mono 64 kbps MP3 of the same length
			mp3, err := os.ReadFile(filepath.Join(e.dir, mp3path))
			if err != nil || int64(len(mp3)) != mp3bytes || len(mp3) < 8000 || len(mp3) > 24000 {
				t.Errorf("mp3 %d bytes (%v), row says %d", len(mp3), err, mp3bytes)
			}
			if ents, _ := fs.Glob(e.root.FS(), "incoming/voice-*"); len(ents) != 0 {
				t.Errorf("work dir left behind: %v", ents)
			}
		})
	}
}

func TestVoice_Invalid(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	e := newEnv(t, "")
	ctx := auth.WithPrincipal(context.Background(), human)
	good := sine(t, 1, "-c:a", "libopus", "-b:a", "16k", "-f", "webm")
	garbage := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("A_OPUS this is not a matroska file at all")...)
	long := sine(t, 130, "-c:a", "libopus", "-b:a", "6k", "-f", "webm")
	if len(long) >= voice.MaxBytes {
		t.Fatalf("fixture too big: %d", len(long))
	}
	for name, tc := range map[string]struct {
		in     voice.Input
		track  string
		status int
		code   string
		reason string
	}{
		"not base64":  {voice.Input{AudioBase64: "!!!not base64!!!"}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonEncoding},
		"empty":       {voice.Input{AudioBase64: ""}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonEncoding},
		"too big":     {voice.Input{AudioBase64: b64(make([]byte, voice.MaxBytes+1))}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonSize},
		"huge string": {voice.Input{AudioBase64: strings.Repeat("A", 8<<20)}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonSize},
		"wav":         {voice.Input{AudioBase64: b64([]byte("RIFF\x24\x00\x00\x00WAVEfmt " + strings.Repeat("x", 40)))}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonContainer},
		"garbage":     {voice.Input{AudioBase64: b64(garbage)}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonDecode},
		"too long":    {voice.Input{AudioBase64: b64(long)}, lt.TrackShort, 422, "VOICE_INVALID", voice.ReasonTooLong},
		"bad kind":    {voice.Input{AudioBase64: b64(good), Kind: "marker"}, lt.TrackShort, 422, "VALIDATION", ""},
		"unknown":     {voice.Input{AudioBase64: b64(good)}, "01900000-0000-7000-8000-00000000ffff", 0, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.Create(ctx, human, "web", tc.track, tc.in)
			if tc.status == 0 {
				if !errors.Is(err, library.ErrNotFound) {
					t.Fatalf("err = %v, want not found", err)
				}
				return
			}
			voiceCode(t, err, tc.status, tc.code, tc.reason)
			if strings.Contains(err.Error(), "A_OPUS") || strings.Contains(err.Error(), e.dir) {
				t.Errorf("error leaks input or path: %v", err)
			}
		})
	}
	var n int
	_ = e.st.R.QueryRow(`SELECT COUNT(*) FROM voice_notes`).Scan(&n)
	if n != 0 {
		t.Errorf("%d voice notes stored by failed uploads", n)
	}
	if got, _ := fs.Glob(e.root.FS(), "voice/*/*"); len(got) != 0 {
		t.Errorf("failed uploads left files: %v", got)
	}
	if got, _ := fs.Glob(e.root.FS(), "incoming/voice-*"); len(got) != 0 {
		t.Errorf("work dirs left behind: %v", got)
	}
}

func TestVoice_AtSecond(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	e := newEnv(t, "")
	ctx := auth.WithPrincipal(context.Background(), human)
	b := b64(sine(t, 1, "-c:a", "libopus", "-b:a", "16k", "-f", "webm"))
	_, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b, AtSecond: at(99999)})
	var je *judgments.Error
	if !errors.As(err, &je) || je.Code != "AT_SECOND_OUT_OF_RANGE" {
		t.Fatalf("err = %v", err)
	}
	id, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b, Body: "keep this"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.judg.CommentByID(ctx, id)
	if c.AtSecond != nil || c.Kind != "note" || c.Body != "keep this" || c.Voice == nil {
		t.Fatalf("untimed note: %+v", c)
	}
	var atSec *float64
	_ = e.st.R.QueryRow(`SELECT at_second FROM voice_notes WHERE id = ?`, c.Voice.ID).Scan(&atSec)
	if atSec != nil {
		t.Errorf("voice row at_second = %v, want NULL", *atSec)
	}
}

func TestVoice_HumanOnly(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	e := newEnv(t, "")
	b := b64(sine(t, 1, "-c:a", "libopus", "-b:a", "16k", "-f", "webm"))
	_, err := e.svc.Create(auth.WithPrincipal(context.Background(), agent), agent, "api", lt.TrackShort, voice.Input{AudioBase64: b})
	voiceCode(t, err, 403, "HUMAN_ONLY", "")
	if _, err := e.svc.Create(auth.WithPrincipal(context.Background(), device), device, "web", lt.TrackShort, voice.Input{AudioBase64: b}); err != nil {
		t.Fatalf("a paired device records: %v", err)
	}
	var kind string
	_ = e.st.R.QueryRow(`SELECT principal_kind FROM voice_notes`).Scan(&kind)
	if kind != "device" {
		t.Errorf("principal_kind = %q", kind)
	}
}

// TestVoice_OriginalsNotServed: only a registered MP3 rendition resolves; the uploaded original, other extensions and files
// that merely sit under voice/ are not found.
func TestVoice_OriginalsNotServed(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	e := newEnv(t, "")
	ctx := auth.WithPrincipal(context.Background(), human)
	id, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b64(sine(t, 1, "-c:a", "libopus", "-b:a", "16k", "-f", "webm"))})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.judg.CommentByID(ctx, id)
	var sha, msha string
	_ = e.st.R.QueryRow(`SELECT sha256, mp3_sha256 FROM voice_notes WHERE id = ?`, c.Voice.ID).Scan(&sha, &msha)
	o, err := e.svc.Resolve(ctx, msha, "mp3")
	if err != nil || o.ContentType != "audio/mpeg" || o.ETag != msha || !strings.HasPrefix(o.RelPath, "voice/") {
		t.Fatalf("mp3: %+v %v", o, err)
	}
	for _, ext := range []string{"webm", "ogg", "mp4", "opus", "flac"} {
		if _, err := e.svc.Resolve(ctx, sha, ext); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("original .%s resolves: %v", ext, err)
		}
		if _, err := e.svc.Resolve(ctx, msha, ext); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("rendition address with .%s resolves: %v", ext, err)
		}
	}
	if _, err := e.svc.Resolve(ctx, sha, "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("original sha as mp3 resolves: %v", err)
	}
	// an unregistered file under voice/ is not an object
	stray := strings.Repeat("ab", 32)
	if err := e.root.MkdirAll("voice/ab", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.root.WriteFile("voice/ab/"+stray+".mp3", []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{stray, "../" + sha, "ABC", strings.ToUpper(msha)} {
		if _, err := e.svc.Resolve(ctx, s, "mp3"); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("%q resolves: %v", s, err)
		}
	}
	// end to end through the media handler: the signed link plays, the original address does not
	h := serve.Handler(e.signer, e.root, serve.Chain(e.svc), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", c.Voice.URL, nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/mpeg" || rec.Body.Len() < 1000 {
		t.Fatalf("GET %d %s %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", c.Voice.URL, nil)
	req.Header.Set("Range", "bytes=0-9")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Errorf("range: %d", rec.Code)
	}
	link := e.signer.Sign(sha, "webm", 1e12, 1e12).URL
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", link, nil))
	if rec.Code != 404 {
		t.Errorf("signed link to the original answers %d, want 404", rec.Code)
	}
}

// TestVoice_Immutable: the tables refuse rewrites and deletes, and the same audio uploaded twice shares one stored object.
func TestVoice_Immutable(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	e := newEnv(t, "")
	ctx := auth.WithPrincipal(context.Background(), human)
	in := voice.Input{AudioBase64: b64(sine(t, 1, "-c:a", "libopus", "-b:a", "16k", "-f", "webm"))}
	id1, err := e.svc.Create(ctx, human, "web", lt.TrackShort, in)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := e.svc.Create(ctx, human, "web", lt.TrackShort, in)
	if err != nil {
		t.Fatalf("same audio again: %v", err)
	}
	c1, _ := e.judg.CommentByID(ctx, id1)
	c2, _ := e.judg.CommentByID(ctx, id2)
	var n, distinct int
	_ = e.st.R.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT sha256) FROM voice_notes`).Scan(&n, &distinct)
	if n != 2 || distinct != 1 || c1.Voice.ID == c2.Voice.ID {
		t.Fatalf("rows %d distinct shas %d", n, distinct)
	}
	if got, _ := fs.Glob(e.root.FS(), "voice/*/*"); len(got) != 2 {
		t.Errorf("stored objects: %v, want the webm and the mp3 once", got)
	}
	for _, q := range []string{
		`UPDATE voice_notes SET duration_s = 1.5`, `DELETE FROM voice_notes`,
		`UPDATE comments SET voice_note_id = NULL WHERE voice_note_id IS NOT NULL`,
	} {
		if _, err := e.st.W.Exec(q); err == nil {
			t.Errorf("%s succeeded", q)
		}
	}
}

// TestVoice_NoVoiceOnOrdinaryComments: comments without a note carry no voice, and without a linker none is attached.
func TestVoice_NoVoiceOnOrdinaryComments(t *testing.T) {
	e := newEnv(t, "")
	ctx := auth.WithPrincipal(context.Background(), human)
	c, err := e.judg.AddComment(ctx, human, "web", lt.TrackShort, judgments.NewComment{Body: "plain"})
	if err != nil || c.Voice != nil {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestRequireVoice(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	if err := voice.RequireVoice(context.Background(), testtools.Tools().FFmpeg); err != nil {
		t.Fatalf("pinned ffmpeg: %v", err)
	}
}

// fakeFFmpeg is a shell ffmpeg: it logs its argv (one line per run) and answers the listings, the decode and the encode.
func fakeFFmpeg(t *testing.T, listings string, decodeBytes int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "argv.log")
	writeFakeTool(t, bin, `
echo "$*" >> `+log+`
for a in "$@"; do
  case "$a" in
    -decoders|-demuxers|-encoders) cat `+filepath.Join(dir, "$a.txt")+` 2>/dev/null; exit 0;;
  esac
done
case "$*" in
  *"-f s16le"*) head -c `+strconv.Itoa(decodeBytes)+` /dev/zero; exit 0;;
  *"-f mp3"*) for a in "$@"; do last="$a"; done; printf 'ID3-synthetic-mp3-bytes-for-the-fake-tool' > "${last#file:}"; exit 0;;
esac
exit 1
`)
	for _, f := range []string{"-decoders", "-demuxers", "-encoders"} {
		if err := os.WriteFile(filepath.Join(dir, f+".txt"), []byte(listings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bin, log
}

const fullListing = ` A....D opus Opus
 A....D vorbis Vorbis
 A....D aac AAC
 A....D pcm_s16le PCM
 D  matroska,webm Matroska
 D  ogg Ogg
 D  mov,mp4,m4a,3gp,3g2,mj2 QuickTime
 A....D libmp3lame MP3
`

// TestVoice_Argv: ffmpeg only ever sees literals and absolute work-dir paths behind file:, never the upload or any caller text,
// and the stored duration comes from the decoded sample count.
func TestVoice_Argv(t *testing.T) {
	t.Parallel()
	bin, log := fakeFFmpeg(t, fullListing, 16000*2*3) // 3.0 s of 16 kHz mono s16le
	e := newEnv(t, bin)
	ctx := auth.WithPrincipal(context.Background(), human)
	upload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("A_OPUS synthetic")...)
	id, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b64(upload), Body: "secret words; rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.judg.CommentByID(ctx, id)
	if c.Voice == nil || c.Voice.DurationS != 3 || c.Body != "secret words; rm -rf /" {
		t.Fatalf("%+v", c)
	}
	raw, _ := os.ReadFile(log)
	runs := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var work int
	for _, line := range runs {
		if strings.Contains(line, "secret") || strings.Contains(line, "A_OPUS") || strings.Contains(line, lt.TrackShort) {
			t.Errorf("caller text in argv: %s", line)
		}
		for _, a := range strings.Fields(line) {
			if strings.HasPrefix(a, "file:") {
				work++
				if p := strings.TrimPrefix(a, "file:"); !filepath.IsAbs(p) || !strings.HasPrefix(p, e.dir+"/incoming/voice-") {
					t.Errorf("path outside the work dir: %s", a)
				}
			}
		}
	}
	if work != 3 { // decode input, encode input, encode output
		t.Errorf("%d file: arguments, want 3: %s", work, raw)
	}
	for _, line := range runs[len(runs)-2:] {
		if !strings.Contains(line, "-protocol_whitelist file,pipe") || !strings.Contains(line, "-nostdin") {
			t.Errorf("missing hardening flags: %s", line)
		}
	}
}

func TestVoice_TooLongByDecodeCap(t *testing.T) {
	t.Parallel()
	bin, _ := fakeFFmpeg(t, fullListing, 125*16000*2+2)
	e := newEnv(t, bin)
	ctx := auth.WithPrincipal(context.Background(), human)
	upload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("A_OPUS synthetic")...)
	_, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b64(upload)})
	voiceCode(t, err, 422, "VOICE_INVALID", voice.ReasonTooLong)
	// 121 s decodes fine but is over the limit
	bin, _ = fakeFFmpeg(t, fullListing, 121*16000*2)
	e = newEnv(t, bin)
	_, err = e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b64(upload)})
	voiceCode(t, err, 422, "VOICE_INVALID", voice.ReasonTooLong)
}

// TestVoice_CapabilityProbe: a missing decoder or encoder makes recording unavailable (ErrToolMissing) while stored notes keep
// resolving; the probe runs once.
func TestVoice_CapabilityProbe(t *testing.T) {
	t.Parallel()
	for name, listing := range map[string]string{
		"no mp3 encoder": strings.Replace(fullListing, "libmp3lame", "libshine", 1),
		"no aac":         strings.Replace(fullListing, " aac ", " aacx ", 1),
		"no mp4 demuxer": strings.Replace(fullListing, "mov,mp4", "mov,xx", 1),
	} {
		t.Run(name, func(t *testing.T) {
			bin, log := fakeFFmpeg(t, listing, 32000)
			e := newEnv(t, bin)
			ctx := auth.WithPrincipal(context.Background(), human)
			upload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("A_OPUS synthetic")...)
			for range 2 {
				_, err := e.svc.Create(ctx, human, "web", lt.TrackShort, voice.Input{AudioBase64: b64(upload)})
				if !errors.Is(err, voice.ErrUnavailable) || !errors.Is(err, analysis.ErrToolMissing) {
					t.Fatalf("err = %v, want unavailable / tool missing", err)
				}
			}
			raw, _ := os.ReadFile(log)
			if n := strings.Count(string(raw), "-decoders"); n != 1 {
				t.Errorf("decoders probed %d times, want once", n)
			}
		})
	}
	// no binary at all
	e := newEnv(t, "/nonexistent/ffmpeg")
	_, err := e.svc.Create(auth.WithPrincipal(context.Background(), human), human, "web", lt.TrackShort, voice.Input{AudioBase64: "AAAA"})
	if !errors.Is(err, voice.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if voice.New(voice.Options{}).Available(context.Background()) == nil {
		t.Error("a bare service claims to be available")
	}
}

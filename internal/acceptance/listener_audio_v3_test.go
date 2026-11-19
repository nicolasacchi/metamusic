package acceptance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// v3Rig is the fixture library whose masters and mp3_320 files carry every kind of hidden metadata (cleantest), with an
// Owner, an Editor and a Listener. The masters are header-valid FLAC files with opaque frames; the media-tools tier
// (internal/media/clean TestFlacCleanWithRealTools) decodes a real one.
func v3Rig(t *testing.T) (r *servertest.Rig, owner, bob, carol *servertest.Client, tn map[string]cleantest.Taint) {
	t.Helper()
	r = servertest.New(t, servertest.Options{Fixture: true, M2c: true, Files: true})
	root, err := os.OpenRoot(r.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	tn = cleantest.Rewrite(t, r.Store, root, lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac, lt.TrackLossy)
	return r, r.Human(t), r.User(t, servertest.UserEditor, config.RoleEditor), r.User(t, servertest.UserListener, config.RoleListener), tn
}

func fetch(t *testing.T, r *servertest.Rig, url string) []byte {
	t.Helper()
	resp, err := http.Get(r.URL + url) //nolint:gosec,noctx // the rig's own loopback server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %.60s: %d %.200s", url, resp.StatusCode, b)
	}
	return b
}

func sum256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func mediaLinks(t *testing.T, c *servertest.Client, id string) map[string]any {
	t.Helper()
	return obj(c.OK(t, "GET", "/api/tracks/"+id+"/media", nil))
}

// TestListenerFlacClean pins C308.7 for the lossless rendition: built on demand from the frozen master, audio frames
// bit-identical, STREAMINFO (and its MD5) and SEEKTABLE kept, one VORBIS_COMMENT with TITLE alone, a hash of its own;
// cached after the first request, built once under concurrency, rebuilt when the title changes; the master is untouched.
func TestListenerFlacClean(t *testing.T) {
	t.Parallel()
	r, owner, _, carol, tn := v3Rig(t)
	id := lt.TrackTwinA
	master, err := os.ReadFile(r.DataDir + "/" + tn[id].MasterPath)
	if err != nil {
		t.Fatal(err)
	}
	st0, _ := os.Stat(r.DataDir + "/" + tn[id].MasterPath)
	rend := r.Services.Renditions
	if rend.Builds() != 0 {
		t.Fatalf("nothing is prebuilt, builds = %d", rend.Builds())
	}
	// concurrent first requests build once
	var wg sync.WaitGroup
	urls := make([]string, 6)
	for i := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			urls[i] = obj(mediaLinks(t, carol, id)["flac"])["url"].(string)
		}()
	}
	wg.Wait()
	if n := rend.Builds(); n != 2 { // flac_clean for the flac link and mp3_320_clean for the mp3 link
		t.Errorf("builds = %d, want 2 (one per kind)", n)
	}
	got := fetch(t, r, urls[0])
	info, err := flacmeta.Read(bytes.NewReader(got), int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	var types []flacmeta.BlockType
	for _, b := range info.Blocks {
		types = append(types, b.Type)
	}
	for _, ty := range types {
		if ty != flacmeta.BlockStreamInfo && ty != flacmeta.BlockSeekTable && ty != flacmeta.BlockPadding && ty != flacmeta.BlockVorbisComment {
			t.Errorf("block type %d in flac_clean (all: %v)", ty, types)
		}
	}
	if len(info.Comments) != 1 || !strings.HasPrefix(info.Comments[0], "TITLE=") {
		t.Errorf("comments %q", info.Comments)
	}
	if !bytes.Equal(got[info.FramesOffset:], tn[id].Frames) || info.StreamInfo.MD5 != tn[id].MD5 {
		t.Errorf("audio frames or STREAMINFO MD5 differ from the master's")
	}
	if sum256(got) == tn[id].MasterSHA || strings.Contains(urls[0], tn[id].MasterSHA) {
		t.Errorf("the Listener's file or URL carries the master's hash")
	}
	if !strings.Contains(urls[0], sum256(got)) {
		t.Errorf("the URL does not carry the rendition's own hash")
	}
	// the master is only read: bytes, mode 0444 and mtime unchanged
	after, _ := os.ReadFile(r.DataDir + "/" + tn[id].MasterPath)
	st1, _ := os.Stat(r.DataDir + "/" + tn[id].MasterPath)
	if !bytes.Equal(after, master) || st1.Mode().Perm() != 0o444 || !st1.ModTime().Equal(st0.ModTime()) {
		t.Errorf("the master changed (mode %v)", st1.Mode())
	}
	// the cache: a second request is a row hit
	built := rend.Builds()
	if again := obj(mediaLinks(t, carol, id)["flac"])["url"].(string); again != urls[0] || rend.Builds() != built {
		t.Errorf("second request rebuilt (builds %d -> %d)", built, rend.Builds())
	}
	// a title change rebuilds both on the next request
	owner.OK(t, "PATCH", "/api/tracks/"+id, map[string]any{"title": "A New Name"})
	renamed := obj(mediaLinks(t, carol, id)["flac"])["url"].(string)
	if renamed == urls[0] || rend.Builds() != built+2 {
		t.Fatalf("title change: url changed %v, builds %d -> %d", renamed != urls[0], built, rend.Builds())
	}
	rb := fetch(t, r, renamed)
	if !bytes.Contains(rb[:len(rb)-len(tn[id].Frames)], []byte("TITLE=A New Name")) {
		t.Errorf("the rebuilt file does not carry the new title")
	}
	// the old address no longer resolves to a file
	if resp, err := http.Get(r.URL + urls[0]); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("the replaced rendition is still served")
		}
	}
}

// TestListenerAudioStripped inspects the actual bytes a Listener can reach, on every route and for both kinds and a lossy
// master: no marker, no MM_* key, no ID3 frame but TIT2, no trailer, the master and its hash never appear, and every
// link is a 15 minute link. The Listener's response shapes have no sha256 field.
func TestListenerAudioStripped(t *testing.T) {
	t.Parallel()
	r, _, _, carol, tn := v3Rig(t)
	for _, id := range []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac, lt.TrackLossy} {
		ml := mediaLinks(t, carol, id)
		dl := obj(carol.OK(t, "GET", "/api/tracks/"+id+"/download", nil))
		raw := string(v3JSON(t, ml)) + string(v3JSON(t, dl))
		for _, secret := range []string{tn[id].MasterSHA, "sha256\":\"" + tn[id].MasterSHA} {
			if strings.Contains(raw, secret) {
				t.Errorf("%s: a Listener response carries the master's hash", id)
			}
		}
		if _, has := obj(ml["mp3"])["sha256"]; has {
			t.Errorf("%s: the Listener mp3 link has a sha256 field", id)
		}
		if l := obj(ml["flac"]); l != nil {
			if _, has := l["sha256"]; has {
				t.Errorf("%s: the Listener flac link has a sha256 field", id)
			}
		}
		if _, has := ml["stems"]; has {
			t.Errorf("%s: stems (unstripped files) offered to a Listener", id)
		}
		if ml["ttl_s"] != float64(900) || dl["ttl_s"] != float64(900) {
			t.Errorf("%s: ttl %v / %v, want 900", id, ml["ttl_s"], dl["ttl_s"])
		}
		urls := []struct{ kind, url string }{{clean.KindMP3, obj(ml["mp3"])["url"].(string)}, {"download", dl["url"].(string)}}
		if l := obj(ml["flac"]); l != nil {
			urls = append(urls, struct{ kind, url string }{clean.KindFLAC, l["url"].(string)})
		}
		for _, u := range urls {
			b := fetch(t, r, u.url)
			if strings.Contains(u.url, tn[id].MasterSHA) || sum256(b) == tn[id].MasterSHA {
				t.Errorf("%s %s: the master was served", id, u.kind)
			}
			kind := clean.KindMP3
			if strings.Contains(strings.SplitN(u.url, "?", 2)[0], ".flac") {
				kind = clean.KindFLAC
			}
			if err := clean.Check(kind, bytes.NewReader(b), int64(len(b))); err != nil {
				t.Errorf("%s %s: %v", id, u.kind, err)
			}
			head := b
			if len(head) > 600 {
				head = append(append([]byte{}, b[:600]...), b[len(b)-200:]...)
			}
			for _, bad := range append([]string{cleantest.Marker, "MM_"}, cleantest.HiddenKeys[6:]...) {
				if bytes.Contains(head, []byte(bad)) {
					t.Errorf("%s %s: the bytes carry %q", id, u.kind, bad)
				}
			}
			if kind == clean.KindMP3 && !bytes.HasPrefix(b, []byte("ID3\x04")) {
				t.Errorf("%s %s: no ID3v2.4 title tag", id, u.kind)
			}
		}
		if fn, _ := dl["filename"].(string); strings.Contains(fn, "__") || strings.Contains(fn, "42") || fn == "" {
			t.Errorf("%s: download filename %q", id, fn)
		}
		tags := obj(dl["tags"])
		if tags["genre"] != nil || tags["track_total"] != float64(0) || tags["album"] != "" || tags["title"] == "" {
			t.Errorf("%s: download tags %v list more than the file carries", id, tags)
		}
	}
	// a lossy master has no flac: the lossy flag and a null flac link as for everyone, and its mp3 is a clean copy
	ml := mediaLinks(t, carol, lt.TrackLossy)
	if ml["flac"] != nil || ml["lossy"] != true {
		t.Errorf("lossy media links: %v", ml)
	}
}

// TestOwnerAudioUnchanged pins the other side: the Owner and an Editor still get the master and the mp3_320 rendition
// byte for byte, with the hashes they always saw, 12 hour links, and a tagged download that carries the MM_* tags.
func TestOwnerAudioUnchanged(t *testing.T) {
	t.Parallel()
	r, owner, bob, _, tn := v3Rig(t)
	id := lt.TrackTwinA
	master, err := os.ReadFile(r.DataDir + "/" + tn[id].MasterPath)
	if err != nil {
		t.Fatal(err)
	}
	var mp3Path string
	if err := r.Store.R.QueryRow(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, id).Scan(&mp3Path); err != nil {
		t.Fatal(err)
	}
	mp3, _ := os.ReadFile(r.DataDir + "/" + mp3Path)
	for name, c := range map[string]*servertest.Client{"owner": owner, "editor": bob} {
		ml := mediaLinks(t, c, id)
		fl, mp := obj(ml["flac"]), obj(ml["mp3"])
		if fl["sha256"] != tn[id].MasterSHA || mp["sha256"] != sum256(mp3) || ml["ttl_s"] != float64(12*3600) {
			t.Errorf("%s: media links %v", name, ml)
		}
		if !bytes.Equal(fetch(t, r, fl["url"].(string)), master) {
			t.Errorf("%s: the flac link is not the master byte for byte", name)
		}
		if !bytes.Equal(fetch(t, r, mp["url"].(string)), mp3) {
			t.Errorf("%s: the mp3 link is not mp3_320 byte for byte", name)
		}
		dl := obj(c.OK(t, "GET", "/api/tracks/"+id+"/download", nil))
		b := fetch(t, r, dl["url"].(string))
		if !bytes.Contains(b[:len(b)-len(tn[id].Frames)], []byte("MM_TAKE_ID=")) || !strings.Contains(dl["filename"].(string), "__") {
			t.Errorf("%s: the tagged download changed (filename %v)", name, dl["filename"])
		}
	}
	if r.Services.Renditions.Builds() != 0 {
		t.Errorf("an Owner or Editor request built a clean rendition")
	}
}

func v3JSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

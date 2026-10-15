package subsonic

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	lunaFLAC = "fixture-master-0"
	lunaMP3  = "fixture-mp3-0"
)

func TestSubsonic_StreamFormats(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.writeFixtureFiles(t)
	mp3Key := x.newClient(t, "mp3client", true, false)
	tests := []struct {
		name   string
		key    string
		kv     []string
		ct     string
		body   string
		status int
	}{
		{"raw", x.rwKey, []string{"format", "raw"}, "audio/flac", lunaFLAC, 200},
		{"flac", x.rwKey, []string{"format", "flac"}, "audio/flac", lunaFLAC, 200},
		{"original", x.rwKey, []string{"format", "original"}, "audio/flac", lunaFLAC, 200},
		{"mp3", x.rwKey, []string{"format", "mp3"}, "audio/mpeg", lunaMP3, 200},
		{"none default raw", x.rwKey, nil, "audio/flac", lunaFLAC, 200},
		{"none maxBitRate 128", x.rwKey, []string{"maxBitRate", "128"}, "audio/mpeg", lunaMP3, 200},
		{"none maxBitRate 320", x.rwKey, []string{"maxBitRate", "320"}, "audio/mpeg", lunaMP3, 200},
		{"none maxBitRate 0 is unlimited", x.rwKey, []string{"maxBitRate", "0"}, "audio/flac", lunaFLAC, 200},
		{"none maxBitRate 500 is above mp3", x.rwKey, []string{"maxBitRate", "500"}, "audio/flac", lunaFLAC, 200},
		{"opus becomes mp3", x.rwKey, []string{"format", "opus"}, "audio/mpeg", lunaMP3, 200},
		{"explicit flac beats maxBitRate", x.rwKey, []string{"format", "flac", "maxBitRate", "128"}, "audio/flac", lunaFLAC, 200},
		{"client default mp3", mp3Key, nil, "audio/mpeg", lunaMP3, 200},
		{"client default mp3, raw asked", mp3Key, []string{"format", "raw"}, "audio/flac", lunaFLAC, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := x.raw("GET", "stream", keyParams(tc.key, append([]string{"id", luna}, tc.kv...)...), nil)
			statusOK(t, w, tc.status)
			eq(t, "content-type", w.Header().Get("Content-Type"), tc.ct)
			eq(t, "body", w.Body.String(), tc.body)
			eq(t, "cache-control", w.Header().Get("Cache-Control"), "private, no-cache")
			eq(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "nosniff")
		})
	}
	// transcodedSuffix shows in getSong for the mp3-default client only
	d := x.get("getSong", keyParams(mp3Key, "id", luna), libIP).doc(t)
	eq(t, "transcodedSuffix", dig(t, d, "song", "transcodedSuffix"), "mp3")
	d = x.call("getSong", "id", luna)
	if v := dig(t, d, "song"); v.(map[string]any)["transcodedSuffix"] != nil {
		t.Errorf("raw client sees transcodedSuffix %v", v)
	}
	// errors
	eq(t, "unknown id", x.get("stream", x.rwq("id", "no-such"), libIP).code(t), 70)
	eq(t, "album id", x.get("stream", x.rwq("id", al(luna)), libIP).code(t), 70)
	eq(t, "missing id", x.get("stream", x.rwq(), libIP).code(t), 10)
}

func TestSubsonic_StreamRangeMatrix(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.writeFixtureFiles(t)
	q := x.rwq("id", luna)
	get := func(hdr map[string]string) *httptestRec { return x.raw("GET", "stream", q, hdr) }
	t.Run("full", func(t *testing.T) {
		w := get(nil)
		statusOK(t, w, 200)
		eq(t, "accept-ranges", w.Header().Get("Accept-Ranges"), "bytes")
		eq(t, "length", w.Header().Get("Content-Length"), len(lunaFLAC))
		if w.Header().Get("ETag") == "" {
			t.Error("no ETag")
		}
	})
	t.Run("range", func(t *testing.T) {
		w := get(map[string]string{"Range": "bytes=2-5"})
		statusOK(t, w, 206)
		eq(t, "content-range", w.Header().Get("Content-Range"), "bytes 2-5/16")
		eq(t, "body", w.Body.String(), lunaFLAC[2:6])
	})
	t.Run("open range", func(t *testing.T) {
		w := get(map[string]string{"Range": "bytes=10-"})
		statusOK(t, w, 206)
		eq(t, "body", w.Body.String(), lunaFLAC[10:])
	})
	t.Run("suffix range", func(t *testing.T) {
		w := get(map[string]string{"Range": "bytes=-4"})
		statusOK(t, w, 206)
		eq(t, "body", w.Body.String(), lunaFLAC[12:])
	})
	t.Run("unsatisfiable", func(t *testing.T) {
		w := get(map[string]string{"Range": "bytes=100-"})
		statusOK(t, w, 416)
		eq(t, "content-range", w.Header().Get("Content-Range"), "bytes */16")
	})
	t.Run("multi range", func(t *testing.T) {
		w := get(map[string]string{"Range": "bytes=0-1,4-5"})
		statusOK(t, w, 206)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "multipart/byteranges") {
			t.Errorf("content-type %q", ct)
		}
	})
	t.Run("if-range match", func(t *testing.T) {
		etag := get(nil).Header().Get("ETag")
		w := get(map[string]string{"Range": "bytes=0-3", "If-Range": etag})
		statusOK(t, w, 206)
	})
	t.Run("if-range mismatch", func(t *testing.T) {
		w := get(map[string]string{"Range": "bytes=0-3", "If-Range": `"other"`})
		statusOK(t, w, 200)
		eq(t, "body", w.Body.String(), lunaFLAC)
	})
	t.Run("if-none-match", func(t *testing.T) {
		etag := get(nil).Header().Get("ETag")
		statusOK(t, get(map[string]string{"If-None-Match": etag}), 304)
	})
	t.Run("head", func(t *testing.T) {
		w := x.raw("HEAD", "stream", q, nil)
		statusOK(t, w, 200)
		eq(t, "length", w.Header().Get("Content-Length"), len(lunaFLAC))
		eq(t, "body", w.Body.Len(), 0)
	})
	t.Run("form post", func(t *testing.T) {
		// the same endpoint over POST with the id in the form still streams
		w := x.raw("POST", "stream", q, nil)
		statusOK(t, w, 200)
	})
}

type httptestRec = httptest.ResponseRecorder

func TestSubsonic_DownloadCurrentTags(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	w := x.raw("GET", "download", x.rwq("id", luna), nil)
	statusOK(t, w, 200)
	eq(t, "disposition", w.Header().Get("Content-Disposition"), `attachment; filename="slug__7.flac"`)
	eq(t, "content-type", w.Header().Get("Content-Type"), "audio/flac")
	eq(t, "cache-control", w.Header().Get("Cache-Control"), "private, no-cache")
	eq(t, "body", w.Body.String(), "tagged-"+luna)
	eq(t, "asked", strings.Join(x.dls.asked, ","), luna)
	eq(t, "unknown", x.get("download", x.rwq("id", "no-such"), libIP).code(t), 70)
	eq(t, "album", x.get("download", x.rwq("id", al(luna)), libIP).code(t), 70)
	eq(t, "missing", x.get("download", x.rwq(), libIP).code(t), 10)
	// a discarded take stays downloadable by id
	x.setStatus(citta, "discarded", x.clk.now())
	statusOK(t, x.raw("GET", "download", x.rwq("id", citta), nil), 200)
}

func TestSubsonic_CoverArt(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	cases := []struct{ id, size, want string }{
		{al(twinA), "", "album:" + twinA + ":512"},
		{coverTrackID(luna), "300", "track:" + luna + ":320"},
		{luna, "300", "track:" + luna + ":320"},
		{luna, "10", "track:" + luna + ":32"},
		{luna, "5000", "track:" + luna + ":1024"},
	}
	for _, c := range cases {
		q := x.rwq("id", c.id)
		if c.size != "" {
			q.Set("size", c.size)
		}
		w := x.raw("GET", "getCoverArt", q, nil)
		statusOK(t, w, 200)
		eq(t, "content-type "+c.id, w.Header().Get("Content-Type"), "image/png")
		eq(t, "cache-control", w.Header().Get("Cache-Control"), "private, max-age=86400")
		eq(t, "asked "+c.id, x.covers.last(), c.want)
	}
	for _, id := range []string{"al-nope", "tr-nope", "nope", artistID} {
		eq(t, "unknown "+id, x.get("getCoverArt", x.rwq("id", id), libIP).code(t), 70)
	}
	eq(t, "missing", x.get("getCoverArt", x.rwq(), libIP).code(t), 10)
}

func TestSubsonic_LossyMasterStreamsMP3(t *testing.T) {
	t.Parallel()
	x, lossy, master := newArchiveRig(t)
	for _, kv := range [][]string{{"format", "raw"}, {"format", "flac"}, {"format", "mp3"}, {"maxBitRate", "128"}, {"maxBitRate", "0"}, nil} {
		w := x.raw("GET", "stream", x.rwq(append([]string{"id", lossy}, kv...)...), nil)
		statusOK(t, w, 200)
		eq(t, "content-type", w.Header().Get("Content-Type"), "audio/mpeg")
		if w.Body.String() != string(master) {
			t.Fatalf("stream %v is not the master (%d vs %d bytes)", kv, w.Body.Len(), len(master))
		}
	}
	mp3Key := x.newClient(t, "mp3client", true, false)
	for _, key := range []string{x.rwKey, mp3Key} {
		d := x.get("getSong", keyParams(key, "id", lossy), libIP).doc(t)
		s := dig(t, d, "song").(map[string]any)
		eq(t, "suffix", s["suffix"], "mp3")
		eq(t, "contentType", s["contentType"], "audio/mpeg")
		for _, k := range []string{"bitDepth", "transcodedSuffix", "transcodedContentType"} {
			if s[k] != nil {
				t.Errorf("%s = %v on a lossy track", k, s[k])
			}
		}
		eq(t, "size", int(s["size"].(float64)), len(master))
	}
	// download is the master unchanged (the fake source is not consulted for a lossy master)
	x.srv.dls = nil
	w := x.raw("GET", "download", x.rwq("id", lossy), nil)
	statusOK(t, w, 200)
	if w.Body.String() != string(master) {
		t.Error("download of a lossy track is not the master")
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=\"") || !strings.HasSuffix(cd, ".mp3\"") {
		t.Errorf("disposition %q", cd)
	}
}

// newArchiveRig is a rig over a copy of the synthetic reference archive; it returns a lossy track and its master bytes.
func newArchiveRig(t *testing.T) (*s3rig, string, []byte) {
	t.Helper()
	tpl := archivetest.SyntheticArchive(t, "reference")
	dir := tpl.Copy(t)
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := newRig(t, rigOpts{st: st})
	x := withServices(t, r, dir)
	var id, path string
	if err := st.R.QueryRow(`SELECT id, master_path FROM tracks WHERE lossy = 1 ORDER BY id LIMIT 1`).Scan(&id, &path); err != nil {
		t.Fatal(err)
	}
	master, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	return x, id, master
}

func TestSubsonic_ListenFormatMP3ForLossy(t *testing.T) {
	t.Parallel()
	x, lossy, _ := newArchiveRig(t)
	statusOK(t, x.raw("GET", "stream", x.rwq("id", lossy, "format", "raw"), nil), 200)
	eq(t, "scrobble", x.get("scrobble", x.rwq("id", lossy), libIP).code(t), 0)
	var f string
	if err := x.st.R.QueryRow(`SELECT format FROM listen_events WHERE track_id = ?`, lossy).Scan(&f); err != nil {
		t.Fatal(err)
	}
	eq(t, "format", f, "mp3")
}

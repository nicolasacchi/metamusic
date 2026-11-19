package acceptance_test

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clean"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// hiddenMMKeys are the Vorbis comment keys that carry the recipe (D-U1-17); no file a Listener can fetch carries one.
var hiddenMMKeys = []string{"MM_SEED", "MM_REQUEST_ID", "MM_RECIPE_ID"}

// TestIntBListenerAudioEveryRoute (D-U1-17, C308.5, C308.7, C314.5): every audio file a Listener can reach, by REST links,
// downloads, clips, a tape and Subsonic streams and downloads in every format, as the browser, a paired phone and a Subsonic
// client, is never a master or any other file whose hash the Listener must not know, carries no hidden tag, and the track
// renditions pass the allow-list (TITLE alone).
func TestIntBListenerAudioEveryRoute(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	forbidden := map[string]string{} // sha256 -> what it is
	for _, s := range intbSQLStrings(t, w.R, `SELECT master_sha256 FROM tracks`) {
		forbidden[s] = "a master"
	}
	for _, s := range intbSQLStrings(t, w.R, `SELECT sha256 FROM renditions WHERE kind NOT LIKE '%clean'`) {
		forbidden[s] = "an Owner rendition"
	}
	checkBytes := func(where string, b []byte, track bool) {
		t.Helper()
		if what, bad := forbidden[sum256(b)]; bad {
			t.Errorf("%s: serves %s", where, what)
		}
		if len(b) > 4 && (bytes.HasPrefix(b, []byte("fLaC")) || bytes.HasPrefix(b, []byte("ID3"))) && track {
			kind := clean.KindMP3
			if bytes.HasPrefix(b, []byte("fLaC")) {
				kind = clean.KindFLAC
			}
			if err := clean.Check(kind, bytes.NewReader(b), int64(len(b))); err != nil {
				t.Errorf("%s: %v", where, err)
			}
		}
		for _, bad := range append([]string{cleantest.Marker}, hiddenMMKeys...) {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s: carries %q", where, bad)
			}
		}
		for id, tt := range w.Taint {
			if len(tt.Frames) > 64 && bytes.Contains(b, tt.Frames) && !track {
				t.Errorf("%s: carries the audio frames of the master of %s unrewritten", where, id)
			}
		}
	}
	web := map[string]*servertest.Client{"browser": w.Carol, "phone": w.R.DeviceFor(t, "carol")}
	routes := 0
	for name, c := range web {
		for id := range w.Taint {
			if !contains(w.Visible, id) {
				continue
			}
			ml := mediaLinks(t, c, id)
			for _, k := range []string{"flac", "mp3"} {
				if l := obj(ml[k]); l != nil {
					b, st := w.fetchMedia(t, l["url"].(string))
					routes++
					if st != 200 {
						t.Errorf("%s %s %s: %d", name, id, k, st)
						continue
					}
					checkBytes(name+" "+id+" "+k, b, true)
				}
			}
			dl := obj(c.OK(t, "GET", "/api/tracks/"+id+"/download", nil))
			b, st := w.fetchMedia(t, dl["url"].(string))
			routes++
			if st != 200 {
				t.Errorf("%s %s download: %d", name, id, st)
			} else {
				checkBytes(name+" "+id+" download", b, true)
			}
			// a clip is a decode and re-encode; whatever it answers, it is no master
			resp := c.Do(t, "GET", "/api/tracks/"+id+"/clip?from_s=0&to_s=3", nil)
			if resp.Status == 200 {
				for _, u := range intbMediaURLs(resp.Body) {
					if b, st := w.fetchMedia(t, u); st == 200 {
						routes++
						checkBytes(name+" "+id+" clip", b, false)
					}
				}
			}
		}
		// the tape: the one built without the liner is a link for a Listener; the one built with it is not
		for _, id := range []string{w.Tape, w.TapeNoLiner} {
			tp := obj(c.OK(t, "GET", "/api/tapes/"+id, nil))
			d := obj(tp["download"])
			if id == w.Tape && d != nil {
				t.Errorf("%s: a tape built with the liner is a link for a Listener", name)
			}
			if id == w.TapeNoLiner {
				if d == nil {
					t.Errorf("%s: a tape built without the liner is no link for a Listener", name)
					continue
				}
				b, st := w.fetchMedia(t, d["url"].(string))
				routes++
				if st != 200 {
					t.Errorf("%s tape: %d", name, st)
					continue
				}
				checkBytes(name+" tape", b, false)
				info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
				if err != nil {
					t.Fatal(err)
				}
				for _, cm := range info.Comments {
					if strings.HasPrefix(cm, "LINER_NOTES=") || strings.Contains(cm, w.LinerDesc) || strings.Contains(cm, canaryLiner) {
						t.Errorf("%s tape carries the liner: %.80s", name, cm)
					}
				}
			}
		}
	}
	for _, write := range []bool{false, true} {
		sub := w.R.SubsonicClientFor(t, "carol", subsonictest.ClientOpts{Name: "carol audio app", Write: write, Username: map[bool]string{false: "carol-audio-ro", true: "carol-audio-rw"}[write]})
		for id := range w.Taint {
			if !contains(w.Visible, id) {
				continue
			}
			for _, f := range []string{"", "raw", "flac", "mp3", "original", "opus"} {
				q := url.Values{"id": {id}}
				if f != "" {
					q.Set("format", f)
				}
				for _, ep := range []string{"stream", "download"} {
					resp := sub.Call(t, ep, q, subsonictest.CallOpts{})
					routes++
					if resp.HTTP != 200 {
						t.Errorf("subsonic %s %s format %q: %d", ep, id, f, resp.HTTP)
						continue
					}
					checkBytes("subsonic "+ep+" "+id+" "+f, resp.Body, true)
				}
			}
			for _, br := range []string{"1", "128", "320"} {
				resp := sub.Call(t, "stream", url.Values{"id": {id}, "maxBitRate": {br}}, subsonictest.CallOpts{})
				routes++
				checkBytes("subsonic stream "+id+" maxBitRate "+br, resp.Body, true)
			}
		}
	}
	if routes < 60 {
		t.Errorf("only %d audio routes were fetched", routes)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Package cleantest is TEST-ONLY: it rewrites the fixture's media files into files that carry every kind of hidden
// metadata a real master or mp3_320 rendition could, so tests can assert that a Listener's clean rendition drops it
// all. Import it only from _test.go files (see TestNoTestHelpersInBinary).
package cleantest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Marker is planted in every free-text tag of the files written here (lyrics, comment, description, ID3 TXXX). A clean
// rendition must not contain it anywhere.
const Marker = "PRIVATE-MARKER-lyrics-recipe-note"

// HiddenKeys are the Vorbis comment keys and ID3 TXXX descriptions planted next to the marker.
var HiddenKeys = []string{"MM_SEED", "MM_REQUEST_ID", "MM_RECIPE_ID", "MM_TAKE_ID", "MM_STATUS", "MM_REFERENCE", "LYRICS", "COMMENT", "DESCRIPTION", "ARTIST", "ALBUM", "GENRE", "REPLAYGAIN_TRACK_GAIN"}

// Taint describes one rewritten track.
type Taint struct {
	ID         string
	Frames     []byte   // the audio bytes of the FLAC master (a lossy master: the MPEG frames)
	MD5        [16]byte // STREAMINFO MD5 of the FLAC master
	MasterPath string
	MasterSHA  string
	MP3Frames  []byte // the MPEG frames of the mp3_320 rendition ("" for a lossy track)
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// MPEGFrames returns n valid MPEG-1 layer III frames (320 kbps, 48 kHz, 960 bytes each), filled with seed.
func MPEGFrames(n int, seed byte) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		f := bytes.Repeat([]byte{seed}, 960)
		copy(f, []byte{0xff, 0xfb, 0xe4, 0x00})
		out = append(out, f...)
	}
	return out
}

func syncsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

func frame(id string, data []byte) []byte {
	out := append([]byte(id), syncsafe(len(data))...)
	out = append(out, 0, 0)
	return append(out, data...)
}

func text(id, v string) []byte { return frame(id, append([]byte{3}, v...)) }

func taintedID3() []byte {
	var body []byte
	body = append(body, text("TIT2", "Hidden "+Marker)...)
	body = append(body, text("TPE1", "Artist "+Marker)...)
	body = append(body, text("COMM", Marker)...)
	for _, k := range []string{"MM_SEED", "MM_REQUEST_ID", "MM_RECIPE_ID"} {
		body = append(body, frame("TXXX", append(append([]byte{3}, k...), append([]byte{0}, Marker...)...))...)
	}
	out := append([]byte{'I', 'D', '3', 4, 0, 0}, syncsafe(len(body))...)
	return append(out, body...)
}

// MP3 returns an MP3 file: a tainted ID3v2.4 tag, n MPEG frames, an ID3v1 and an APEv2 trailer.
func MP3(n int, seed byte) []byte {
	out := append(taintedID3(), MPEGFrames(n, seed)...)
	out = append(out, append([]byte("TAG"), bytes.Repeat([]byte(Marker[:1]), 125)...)...)
	return out
}

// Rewrite replaces the master of each lossless id (and its mp3_320 rendition file and row) with a tainted file, and a
// lossy id's MP3 master. It lifts the frozen-track trigger of the test database, so use it on a test store only.
func Rewrite(tb testing.TB, st *store.Store, data *os.Root, ids ...string) map[string]Taint {
	tb.Helper()
	if _, err := st.W.Exec(`DROP TRIGGER IF EXISTS tracks_frozen`); err != nil {
		tb.Fatal(err)
	}
	out := map[string]Taint{}
	for i, id := range ids {
		var path string
		var lossy int
		if err := st.R.QueryRow(`SELECT master_path, lossy FROM tracks WHERE id = ?`, id).Scan(&path, &lossy); err != nil {
			tb.Fatal(err)
		}
		t := Taint{ID: id, MasterPath: path}
		var master []byte
		if lossy == 1 {
			t.Frames = MPEGFrames(4, byte(i+1))
			master = append(append(taintedID3(), t.Frames...), []byte("TAG")...)
		} else {
			t.Frames = bytes.Repeat([]byte{0xff, 0xf8, byte(i + 1)}, 2000)
			t.MD5 = [16]byte{byte(i + 1), 7, 7, 7}
			comments := []string{"TITLE=Hidden " + Marker, "MM_TAKE_ID=" + id, "MM_STATUS=draft", "MM_SEED=42", "MM_REQUEST_ID=" + Marker, "MM_RECIPE_ID=r-" + id}
			for _, k := range HiddenKeys[6:] {
				comments = append(comments, k+"="+Marker)
			}
			master = flactest.Build(flactest.Spec{TotalSamples: 96000, MD5: t.MD5, Vendor: "ingest", SeekPoints: 4, PaddingBytes: 256, PictureBytes: 300, Comments: comments, Frames: t.Frames})
		}
		put(tb, data, path, master)
		t.MasterSHA = sum(master)
		if _, err := st.W.Exec(`UPDATE tracks SET master_sha256 = ?, master_bytes = ?, frames_sha256 = ?, audio_md5 = ? WHERE id = ?`,
			t.MasterSHA, len(master), sum(t.Frames), hex.EncodeToString(t.MD5[:]), id); err != nil {
			tb.Fatal(err)
		}
		if lossy == 0 {
			var rp string
			if err := st.R.QueryRow(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, id).Scan(&rp); err != nil {
				tb.Fatal(fmt.Errorf("track %s has no mp3_320 row: %w", id, err))
			}
			t.MP3Frames = MPEGFrames(5, byte(i+11))
			mp3 := append(taintedID3(), t.MP3Frames...)
			put(tb, data, rp, mp3)
			if _, err := st.W.Exec(`UPDATE renditions SET sha256 = ?, bytes = ? WHERE track_id = ? AND kind = 'mp3_320'`, sum(mp3), len(mp3), id); err != nil {
				tb.Fatal(err)
			}
		}
		out[id] = t
	}
	return out
}

func put(tb testing.TB, data *os.Root, rel string, b []byte) {
	tb.Helper()
	_ = data.Chmod(rel, 0o644)
	if err := data.WriteFile(rel, b, 0o444); err != nil {
		tb.Fatal(err)
	}
	if err := data.Chmod(rel, 0o444); err != nil {
		tb.Fatal(err)
	}
}

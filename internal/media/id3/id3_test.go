package id3

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

type frame struct {
	ID   string
	Data []byte
}

// readTag is a minimal ID3v2.4 reader: header, synchsafe sizes, frames.
func readTag(t *testing.T, b []byte) (size int, frames []frame) {
	t.Helper()
	if len(b) < 10 || string(b[:3]) != "ID3" || b[3] != 4 || b[4] != 0 || b[5] != 0 {
		t.Fatalf("bad header % x", b[:min(10, len(b))])
	}
	ss := func(p []byte) int {
		for _, c := range p {
			if c&0x80 != 0 {
				t.Fatalf("size byte %#x not synchsafe", c)
			}
		}
		return int(p[0])<<21 | int(p[1])<<14 | int(p[2])<<7 | int(p[3])
	}
	size = ss(b[6:10])
	if len(b) != 10+size {
		t.Fatalf("tag length %d, header says %d", len(b), 10+size)
	}
	body := b[10:]
	for len(body) > 0 {
		if len(body) < 10 {
			t.Fatalf("trailing %d bytes", len(body))
		}
		n := ss(body[4:8])
		if body[8] != 0 || body[9] != 0 {
			t.Fatalf("frame flags set")
		}
		frames = append(frames, frame{string(body[:4]), body[10 : 10+n]})
		body = body[10+n:]
	}
	return size, frames
}

func text(t *testing.T, f frame) string {
	t.Helper()
	if f.Data[0] != 0x03 {
		t.Fatalf("%s encoding %#x", f.ID, f.Data[0])
	}
	return string(f.Data[1:])
}

func TestID3_Encode(t *testing.T) {
	full := Tag{Title: "Città lenta", Artist: "metamusic", Album: "mix", Track: "2/3", Stars: 3,
		TXXX: [][2]string{{"MM_TAKE_ID", "t1"}, {"MM_REQUEST_ID", "r1"}, {"MM_SEED", "7"}, {"MM_STATUS", "final"}}}
	_, fr := readTag(t, Encode(full))
	var ids []string
	for _, f := range fr {
		ids = append(ids, f.ID)
	}
	if got, want := strings.Join(ids, ","), "TIT2,TPE1,TALB,TRCK,POPM,TXXX,TXXX,TXXX,TXXX"; got != want {
		t.Fatalf("frame order %s, want %s", got, want)
	}
	if got := text(t, fr[0]); got != "Città lenta" || !utf8.ValidString(got) {
		t.Errorf("TIT2 %q", got)
	}
	if text(t, fr[1]) != "metamusic" || text(t, fr[2]) != "mix" || text(t, fr[3]) != "2/3" {
		t.Errorf("text frames wrong")
	}
	want := append([]byte("metamusic\x00"), 128)
	if !bytes.Equal(fr[4].Data, want) {
		t.Errorf("POPM % x, want % x", fr[4].Data, want)
	}
	desc, val, ok := bytes.Cut(fr[5].Data[1:], []byte{0})
	if fr[5].Data[0] != 3 || !ok || string(desc) != "MM_TAKE_ID" || string(val) != "t1" {
		t.Errorf("TXXX %q", fr[5].Data)
	}
}

func TestID3_POPMTable(t *testing.T) {
	for stars, want := range map[int]byte{1: 1, 2: 64, 3: 128, 4: 196, 5: 255} {
		_, fr := readTag(t, Encode(Tag{Title: "x", Stars: stars}))
		var got []byte
		for _, f := range fr {
			if f.ID == "POPM" {
				got = f.Data
			}
		}
		if len(got) == 0 || got[len(got)-1] != want {
			t.Errorf("stars %d: POPM % x, want rating %d", stars, got, want)
		}
	}
	for _, s := range []int{0, 6, -1} {
		_, fr := readTag(t, Encode(Tag{Title: "x", Stars: s}))
		for _, f := range fr {
			if f.ID == "POPM" {
				t.Errorf("stars %d: unexpected POPM", s)
			}
		}
	}
}

func TestID3_SynchsafeSize(t *testing.T) {
	title := strings.Repeat("a", 200)
	b := Encode(Tag{Title: title})
	size, fr := readTag(t, b)
	if size != (10+1+200)+3*(10+1) { // TIT2 plus the empty TPE1, TALB and TRCK
		t.Errorf("tag size %d", size)
	}
	if size < 128 {
		t.Fatal("size must need two synchsafe bytes")
	}
	if text(t, fr[0]) != title {
		t.Errorf("title lost")
	}
}

func TestID3_Sanitises(t *testing.T) {
	_, fr := readTag(t, Encode(Tag{Title: "a\x00b\nc\x7fd", TXXX: [][2]string{{"K", "v\tw"}}}))
	if got := text(t, fr[0]); got != "a b c d" {
		t.Errorf("title %q", got)
	}
	if !bytes.Equal(fr[len(fr)-1].Data, append([]byte{3}, "K\x00v w"...)) {
		t.Errorf("TXXX %q", fr[len(fr)-1].Data)
	}
}

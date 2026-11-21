package dsptest

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// riff walks the chunks of a RIFF body and returns id -> data (LIST chunks keyed "LIST:<kind>").
func riff(t *testing.T, b []byte, out map[string][]byte) {
	t.Helper()
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatalf("truncated chunk header (%d bytes left)", len(b))
		}
		id, n := string(b[:4]), int(binary.LittleEndian.Uint32(b[4:]))
		if 8+n > len(b) {
			t.Fatalf("chunk %s overruns its parent", id)
		}
		data := b[8 : 8+n]
		adv := 8 + n + n%2
		if id == "LIST" {
			out["LIST:"+string(data[:4])] = data[4:]
			riff(t, data[4:], out)
		} else {
			out[id] = data
		}
		if adv > len(b) {
			adv = len(b)
		}
		b = b[adv:]
	}
}

func TestSF2_Structure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		o       SF2Options
		presets int
		relTC   int16
	}{
		{"default", SF2Options{}, 1, -12000},
		{"one second release", SF2Options{Release: time.Second}, 1, 0},
		{"many presets", SF2Options{Presets: 128}, 128, -12000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.sf2")
			if err := WriteSF2(path, tc.o); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b[:4]) != "RIFF" || int(binary.LittleEndian.Uint32(b[4:]))+8 != len(b) || string(b[8:12]) != "sfbk" {
				t.Fatal("bad RIFF header")
			}
			c := map[string][]byte{}
			riff(t, b[12:], c)
			for id, size := range map[string]int{
				"phdr": 38 * (tc.presets + 1), "pbag": 4 * (tc.presets + 1), "pmod": 10, "pgen": 4 * (tc.presets + 1),
				"inst": 22 * 2, "ibag": 4 * 2, "imod": 10, "igen": 4 * 5, "shdr": 46 * 2, "ifil": 4,
			} {
				if len(c[id]) != size {
					t.Errorf("%s: %d bytes, want %d", id, len(c[id]), size)
				}
			}
			if got := len(c["smpl"]); got != 2*(sf2Points+sf2Pad) {
				t.Errorf("smpl %d bytes", got)
			}
			// the last 46 sample points are zero (spec padding) and the loop sits on whole periods
			for _, v := range c["smpl"][2*sf2Points:] {
				if v != 0 {
					t.Fatal("padding is not zero")
				}
			}
			sh := c["shdr"]
			u := func(off int) uint32 { return binary.LittleEndian.Uint32(sh[off:]) }
			if u(20) != 0 || u(24) != sf2Points || u(28) != sf2LoopStart || u(32) != sf2Points || u(36) != sf2Rate || sh[40] != 69 {
				t.Errorf("sample header % x", sh[:46])
			}
			if (u(32)-u(28))%sf2Period != 0 {
				t.Error("loop is not whole periods")
			}
			ig := c["igen"]
			if op := binary.LittleEndian.Uint16(ig[4:]); op != genReleaseVolEnv {
				t.Fatalf("igen[1] op %d", op)
			}
			if rel := int16(binary.LittleEndian.Uint16(ig[6:])); rel != tc.relTC {
				t.Errorf("release %d timecents, want %d", rel, tc.relTC)
			}
			if op := binary.LittleEndian.Uint16(ig[12:]); op != genSampleID {
				t.Errorf("last instrument generator is %d, want sampleID", op)
			}
		})
	}
}

func TestSF2_Options(t *testing.T) {
	for _, o := range []SF2Options{{Presets: -1}, {Presets: 129}, {Release: -time.Second}} {
		if _, err := BuildSF2(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	a, _ := BuildSF2(SF2Options{})
	b, _ := BuildSF2(SF2Options{})
	if string(a) != string(b) {
		t.Error("not deterministic")
	}
}

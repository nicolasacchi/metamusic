package smf

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/score"
)

var update = flag.Bool("update", false, "rewrite testdata/*.hex")

// decoded is what the test-side reader recovers: the Score fields an SMF carries plus the End-of-Track tick of every track.
type decoded struct {
	Format, Tracks, Div int
	Score               score.Score
	EOT                 []int64
	Names               []string
}

type chanPitch struct{ ch, pitch int }

type pendingNote struct {
	tick int64
	vel  int
}

// decode is the ~60-line reader that exists only to verify the writer (format 0/1, running status, tempo, meter, program,
// note-on velocity 0 = off). There is no production reader.
func decode(b []byte) (decoded, error) {
	var d decoded
	if len(b) < 14 || string(b[:4]) != "MThd" || binary.BigEndian.Uint32(b[4:8]) != 6 {
		return d, errors.New("bad header")
	}
	d.Format = int(binary.BigEndian.Uint16(b[8:]))
	d.Tracks = int(binary.BigEndian.Uint16(b[10:]))
	d.Div = int(binary.BigEndian.Uint16(b[12:]))
	d.Score.PPQ = d.Div
	pos := 14
	for ti := 0; ti < d.Tracks; ti++ {
		if pos+8 > len(b) || string(b[pos:pos+4]) != "MTrk" {
			return d, errors.New("bad track header")
		}
		n := int(binary.BigEndian.Uint32(b[pos+4:]))
		tr := b[pos+8 : pos+8+n]
		pos += 8 + n
		var tick int64
		var status byte
		var voice score.Voice
		open := map[chanPitch][]pendingNote{}
		name := ""
		eot := int64(-1)
		i := 0
		vlq := func() int64 {
			var v int64
			for {
				c := tr[i]
				i++
				v = v<<7 | int64(c&0x7f)
				if c&0x80 == 0 {
					return v
				}
			}
		}
		for i < len(tr) {
			tick += vlq()
			if tr[i]&0x80 != 0 {
				status = tr[i]
				i++
			}
			switch {
			case status == 0xFF:
				typ := tr[i]
				i++
				l := int(vlq())
				data := tr[i : i+l]
				i += l
				switch typ {
				case 0x03:
					name = string(data)
				case 0x51:
					d.Score.Tempos = append(d.Score.Tempos, score.Tempo{Tick: tick, USPerQ: uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2])})
				case 0x58:
					d.Score.Meters = append(d.Score.Meters, score.Meter{Tick: tick, Num: int(data[0]), Den: 1 << data[1]})
				case 0x2F:
					eot = tick
				}
			case status>>4 == 0xC:
				voice.Program, voice.Channel = int(tr[i]), int(status&0x0f)
				i++
			case status>>4 == 0x9 || status>>4 == 0x8:
				ch, p, v := int(status&0x0f), int(tr[i]), int(tr[i+1])
				i += 2
				k := chanPitch{ch, p}
				voice.Channel = ch
				if status>>4 == 0x9 && v > 0 {
					open[k] = append(open[k], pendingNote{tick, v})
					continue
				}
				q := open[k]
				if len(q) == 0 {
					return d, fmt.Errorf("note off without note on at %d", tick)
				}
				voice.Notes = append(voice.Notes, score.Note{Pitch: p, Vel: q[0].vel, Tick: q[0].tick, Dur: tick - q[0].tick})
				open[k] = q[1:]
			default:
				return d, fmt.Errorf("unsupported status %x", status)
			}
		}
		for _, q := range open {
			if len(q) > 0 {
				return d, errors.New("hanging note")
			}
		}
		d.EOT = append(d.EOT, eot)
		d.Names = append(d.Names, name)
		if ti > 0 {
			voice.ID, voice.Name = name, name
			sort.SliceStable(voice.Notes, func(a, b int) bool {
				x, y := voice.Notes[a], voice.Notes[b]
				if x.Tick != y.Tick {
					return x.Tick < y.Tick
				}
				return x.Pitch < y.Pitch
			})
			d.Score.Voices = append(d.Score.Voices, voice)
		}
	}
	return d, nil
}

func fixedScore() *score.Score {
	s := &score.Score{
		PPQ:    480,
		Tempos: []score.Tempo{{Tick: 0, USPerQ: 500000}, {Tick: 1920, USPerQ: 400000}},
		Meters: []score.Meter{{Tick: 0, Num: 4, Den: 4}, {Tick: 1920, Num: 6, Den: 8}},
		Voices: []score.Voice{
			{ID: "V1", Name: "V1", Program: 40, Channel: 0, Notes: []score.Note{
				{Pitch: 69, Vel: 80, Tick: 0, Dur: 480}, {Pitch: 64, Vel: 80, Tick: 480, Dur: 960},
				{Pitch: 72, Vel: 80, Tick: 480, Dur: 480}, {Pitch: 67, Vel: 80, Tick: 2400, Dur: 240}}},
			{ID: "V2", Name: "V2", Program: 0, Channel: 1, Notes: []score.Note{
				{Pitch: 48, Vel: 64, Tick: 0, Dur: 1920}, {Pitch: 48, Vel: 64, Tick: 1920, Dur: 480}}},
			{ID: "V3", Name: "V3", Program: 73, Channel: 2, Notes: []score.Note{{Pitch: 84, Vel: 100, Tick: 960, Dur: 960}}},
		},
	}
	return s
}

func TestSMF_RoundTrip(t *testing.T) {
	in := fixedScore()
	d, err := decode(Write(in, 0))
	if err != nil {
		t.Fatal(err)
	}
	if d.Format != 1 || d.Tracks != 4 || d.Div != 480 {
		t.Fatalf("header %+v", d)
	}
	if diff := cmp.Diff(*in, d.Score); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}

func TestSMF_NoteOffBeforeNoteOnAtEqualTicks(t *testing.T) {
	s := &score.Score{PPQ: 480, Voices: []score.Voice{{ID: "A", Name: "A", Notes: []score.Note{
		{Pitch: 60, Vel: 80, Tick: 0, Dur: 480}, {Pitch: 60, Vel: 80, Tick: 480, Dur: 480}}}}}
	if _, err := decode(Write(s, 0)); err != nil {
		t.Fatalf("same-pitch back-to-back notes must decode (off before on): %v", err)
	}
	b := Write(s, 0)
	// the second note-on (0x90 60 80) must come after the first note-off (0x80 60 40): find them in order
	off, on2 := strings.Index(hex.EncodeToString(b), "803c40"), strings.LastIndex(hex.EncodeToString(b), "903c50")
	if off < 0 || on2 < off {
		t.Errorf("note-off at %d, last note-on at %d", off, on2)
	}
}

func TestSMF_ZeroDurationNoteStaysAudible(t *testing.T) {
	s := &score.Score{PPQ: 480, Voices: []score.Voice{{ID: "A", Name: "A", Notes: []score.Note{{Pitch: 60, Vel: 80, Tick: 0, Dur: 0}}}}}
	d, err := decode(Write(s, 0))
	if err != nil || len(d.Score.Voices[0].Notes) != 1 || d.Score.Voices[0].Notes[0].Dur != 1 {
		t.Fatalf("d=%+v err=%v", d.Score.Voices, err)
	}
}

func TestSMF_Golden(t *testing.T) {
	b := Write(fixedScore(), 2)
	var sb strings.Builder
	for i := 0; i < len(b); i += 16 {
		end := min(i+16, len(b))
		fmt.Fprintf(&sb, "%04x  %s\n", i, hex.EncodeToString(b[i:end]))
	}
	path := filepath.Join("testdata", "fixed.hex")
	if *update {
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), sb.String()); diff != "" {
		t.Errorf("golden (-want +got):\n%s", diff)
	}
	if string(Write(fixedScore(), 2)) != string(b) {
		t.Error("Write is not deterministic")
	}
}

func TestSMF_TailEndOfTrack(t *testing.T) {
	s := fixedScore()
	// last note ends at tick 2640; the tempo at the end is 400000 us/quarter = 1200 ticks/s
	for _, tc := range []struct {
		tail float64
		want int64
	}{{0, 2640}, {2, 2640 + 2400}, {0.5, 2640 + 600}, {10, 2640 + 12000}} {
		d, err := decode(Write(s, tc.tail))
		if err != nil {
			t.Fatal(err)
		}
		if len(d.EOT) != 4 {
			t.Fatalf("tracks %d", len(d.EOT))
		}
		for i, e := range d.EOT {
			if e != tc.want {
				t.Errorf("tail %v track %d: End of Track at %d, want %d", tc.tail, i, e, tc.want)
			}
		}
	}
	// a tempo or meter entry after the last note still precedes the End of Track
	s2 := &score.Score{PPQ: 480, Tempos: []score.Tempo{{Tick: 0, USPerQ: 500000}, {Tick: 4000, USPerQ: 500000}},
		Voices: []score.Voice{{ID: "A", Name: "A", Notes: []score.Note{{Pitch: 60, Vel: 80, Tick: 0, Dur: 480}}}}}
	d, err := decode(Write(s2, 0))
	if err != nil || d.EOT[0] != 4000 || d.EOT[1] != 4000 {
		t.Fatalf("eot %v err %v", d.EOT, err)
	}
}

func TestSMF_ClampsAndEmpty(t *testing.T) {
	s := &score.Score{Voices: []score.Voice{{ID: "A", Name: strings.Repeat("n", 400), Program: 300, Channel: 17,
		Notes: []score.Note{{Pitch: 200, Vel: 0, Tick: 0, Dur: 10}}}}}
	d, err := decode(Write(s, -1))
	if err != nil {
		t.Fatal(err)
	}
	n := d.Score.Voices[0]
	if n.Notes[0].Pitch != 127 || n.Notes[0].Vel != 1 || n.Program != 300&0x7f || n.Channel != 17&0x0f || len(d.Names[1]) != 128 {
		t.Errorf("%+v names %d", n, len(d.Names[1]))
	}
	if d, err := decode(Write(&score.Score{}, 0)); err != nil || d.Tracks != 1 || d.Div != 480 {
		t.Errorf("empty score: %+v %v", d, err)
	}
}

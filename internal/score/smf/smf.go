// Package smf writes a score.Score as a Standard MIDI File (format 1, 480 ticks per quarter note) for the synth adapter
// (C353, T1 C208.4). There is no production reader: MIDI upload is not a feature, and the test-side decoder only verifies this
// writer. The output is a pure function of the Score and the tail, so it is deterministic.
package smf

import (
	"encoding/binary"
	"math"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/score"
)

const (
	ticksPerQuarter = score.PPQ
	defaultUSPerQ   = 500000
	maxNameBytes    = 128
	trackZeroName   = "score"
)

// ev is one track event before delta encoding. order sorts events at equal ticks: meta and program events first, then
// note-offs, then note-ons (so a note ending where the next begins never cuts it short).
type ev struct {
	tick  int64
	order int
	pitch int
	data  []byte
}

const (
	orderMeta = iota
	orderOff
	orderOn
)

// Write encodes s. Every track ends with an End of Track at last_tick + round(tailS * ticks per second), where last_tick is
// the latest note end, tempo entry or meter entry of the score and the rate is that of the tempo in force there: this is how
// the render tail (a sustained no-op) reaches the synth. Pitches are clamped to 0..127, velocities to 1..127, a zero-length
// note to one tick, programs and channels to their 7 and 4 bit ranges, track names to 128 bytes.
func Write(s *score.Score, tailS float64) []byte {
	last := lastTick(s)
	eot := last + tailTicks(s, last, tailS)

	out := make([]byte, 0, 64+16*noteCount(s))
	out = append(out, "MThd"...)
	out = binary.BigEndian.AppendUint32(out, 6)
	out = binary.BigEndian.AppendUint16(out, 1)
	out = binary.BigEndian.AppendUint16(out, uint16(1+len(s.Voices))) // #nosec G115 -- voices are capped at score.MaxVoices by Normalise; 65535 tracks is the format limit
	out = binary.BigEndian.AppendUint16(out, ticksPerQuarter)

	out = appendTrack(out, tempoTrack(s), eot)
	for i := range s.Voices {
		out = appendTrack(out, voiceTrack(&s.Voices[i]), eot)
	}
	return out
}

func noteCount(s *score.Score) int {
	n := 0
	for i := range s.Voices {
		n += len(s.Voices[i].Notes)
	}
	return n
}

func dur(n score.Note) int64 { return max(n.Dur, 1) }

func lastTick(s *score.Score) int64 {
	var last int64
	for i := range s.Voices {
		for _, n := range s.Voices[i].Notes {
			last = max(last, n.Tick+dur(n))
		}
	}
	for _, t := range s.Tempos {
		last = max(last, t.Tick)
	}
	for _, m := range s.Meters {
		last = max(last, m.Tick)
	}
	return last
}

// tailTicks converts seconds to ticks at the tempo in force at tick last.
func tailTicks(s *score.Score, last int64, tailS float64) int64 {
	if !(tailS > 0) {
		return 0
	}
	us := uint32(defaultUSPerQ)
	tempos := append([]score.Tempo(nil), s.Tempos...)
	sort.SliceStable(tempos, func(a, b int) bool { return tempos[a].Tick < tempos[b].Tick })
	for _, t := range tempos {
		if t.Tick <= last && t.USPerQ > 0 {
			us = t.USPerQ
		}
	}
	return int64(math.Round(tailS * ticksPerQuarter * 1e6 / float64(us)))
}

func tempoTrack(s *score.Score) []ev {
	evs := []ev{{order: orderMeta, data: meta(0x03, []byte(trackZeroName))}}
	for _, t := range s.Tempos {
		us := min(max(t.USPerQ, 1), 0xFFFFFF)
		evs = append(evs, ev{tick: max(t.Tick, 0), order: orderMeta, data: meta(0x51, []byte{byte(us >> 16 & 0xff), byte(us >> 8 & 0xff), byte(us & 0xff)})})
	}
	for _, m := range s.Meters {
		den := max(m.Den, 1)
		dd := 0
		for 1<<(dd+1) <= den && dd < 7 {
			dd++
		}
		num := byte(min(max(m.Num, 1), 255)) // #nosec G115 -- clamped to 1..255
		evs = append(evs, ev{tick: max(m.Tick, 0), order: orderMeta + 1, data: meta(0x58, []byte{num, byte(dd), 24, 8})})
	}
	return evs
}

func voiceTrack(v *score.Voice) []ev {
	name := v.Name
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	ch := byte(v.Channel & 0x0f)
	evs := []ev{
		{order: orderMeta, data: meta(0x03, []byte(name))},
		{order: orderMeta + 1, data: []byte{0xC0 | ch, byte(v.Program & 0x7f)}},
	}
	for _, n := range v.Notes {
		p := byte(min(max(n.Pitch, 0), 127))
		vel := byte(min(max(n.Vel, 1), 127))
		evs = append(evs,
			ev{tick: max(n.Tick, 0), order: orderOn, pitch: int(p), data: []byte{0x90 | ch, p, vel}},
			ev{tick: max(n.Tick, 0) + dur(n), order: orderOff, pitch: int(p), data: []byte{0x80 | ch, p, 0x40}})
	}
	return evs
}

func meta(typ byte, data []byte) []byte {
	b := append([]byte{0xFF, typ}, vlq(int64(len(data)))...)
	return append(b, data...)
}

func vlq(v int64) []byte {
	if v < 0 {
		v = 0
	}
	b := []byte{byte(v & 0x7f)}
	for v >>= 7; v > 0; v >>= 7 {
		b = append([]byte{byte(v&0x7f) | 0x80}, b...)
	}
	return b
}

// appendTrack sorts evs into a total order (tick, kind, pitch, then input order), delta-encodes them and closes the track.
func appendTrack(out []byte, evs []ev, eot int64) []byte {
	sort.SliceStable(evs, func(a, b int) bool {
		x, y := evs[a], evs[b]
		if x.tick != y.tick {
			return x.tick < y.tick
		}
		if x.order != y.order {
			return x.order < y.order
		}
		return x.pitch < y.pitch
	})
	var body []byte
	var at int64
	for _, e := range evs {
		body = append(body, vlq(e.tick-at)...)
		body = append(body, e.data...)
		at = e.tick
	}
	body = append(body, vlq(eot-at)...)
	body = append(body, 0xFF, 0x2F, 0x00)
	out = append(out, "MTrk"...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(body))) // #nosec G115 -- bounded by score.MaxEvents * 8 bytes
	return append(out, body...)
}

// Package score is the note-event model shared by the ABC toolkit (internal/score/abc), the SMF writer and the synth
// adapter (C351). It imports only the standard library.
package score

import (
	"errors"
	"sort"
)

const (
	// PPQ is the tick resolution of every Score.
	PPQ = 480
	// MaxEvents is the most notes a Score may carry (the transform limit MaxScoreEvents, repeated here because this
	// package imports nothing of the application).
	MaxEvents = 200000
	// MaxVoices is the most voices a Score may carry.
	MaxVoices = 16
	// MaxSeconds is the longest Score (20 minutes).
	MaxSeconds = 1200.0

	defaultUSPerQ = 500000 // 120 quarter notes per minute
)

// Limit errors returned by Normalise.
var (
	ErrTooManyEvents = errors.New("score: too many events")
	ErrTooManyVoices = errors.New("score: too many voices")
	ErrTooLong       = errors.New("score: too long")
)

// Warning is a non-fatal observation about a score (an ignored decoration, an irregular bar, an unknown chord).
type Warning struct {
	Code string `json:"code"`
	Line int    `json:"line"`
}

// Tempo is a tempo-map entry: microseconds per quarter note from Tick on.
type Tempo struct {
	Tick   int64
	USPerQ uint32
}

// Meter is a meter-map entry from Tick on.
type Meter struct {
	Tick     int64
	Num, Den int
}

// Note is one sounding note. Vel is the MIDI velocity, Tick and Dur are in PPQ ticks.
type Note struct {
	Pitch, Vel int
	Tick, Dur  int64
}

// Voice is one track: a name, a General MIDI program and channel, and its notes.
type Voice struct {
	ID, Name         string
	Program, Channel int
	Notes            []Note
}

// Score is a rendered ABC tune as note events.
type Score struct {
	PPQ      int
	Tempos   []Tempo
	Meters   []Meter
	Voices   []Voice
	Bars     int     // bars of the first voice, repeats expanded
	BarTicks []int64 // start tick of each of those bars
	Warnings []Warning
}

// SecondsAt converts a tick to seconds through the tempo map (120 quarter notes per minute before the first entry).
func (s *Score) SecondsAt(tick int64) float64 {
	ppq := float64(s.PPQ)
	if s.PPQ <= 0 {
		ppq = PPQ
	}
	sec := 0.0
	prevTick, us := int64(0), float64(defaultUSPerQ)
	for _, t := range s.Tempos {
		if t.Tick >= tick {
			break
		}
		if t.Tick > prevTick {
			sec += float64(t.Tick-prevTick) * us / ppq / 1e6
			prevTick = t.Tick
		}
		us = float64(t.USPerQ)
	}
	if tick > prevTick {
		sec += float64(tick-prevTick) * us / ppq / 1e6
	}
	return sec
}

// BarStartsS returns the start of each bar of the first voice in seconds.
func (s *Score) BarStartsS() []float64 {
	out := make([]float64, len(s.BarTicks))
	for i, t := range s.BarTicks {
		out[i] = s.SecondsAt(t)
	}
	return out
}

// EndTick is the tick at which the last note ends.
func (s *Score) EndTick() int64 {
	var end int64
	for _, v := range s.Voices {
		for _, n := range v.Notes {
			if e := n.Tick + n.Dur; e > end {
				end = e
			}
		}
	}
	return end
}

// Normalise sorts the maps and every voice's notes by (Tick, Pitch) and enforces the caps: MaxEvents notes, MaxVoices
// voices and MaxSeconds of music.
func (s *Score) Normalise() error {
	if len(s.Voices) > MaxVoices {
		return ErrTooManyVoices
	}
	total := 0
	for i := range s.Voices {
		total += len(s.Voices[i].Notes)
		if total > MaxEvents {
			return ErrTooManyEvents
		}
		ns := s.Voices[i].Notes
		sort.SliceStable(ns, func(a, b int) bool {
			if ns[a].Tick != ns[b].Tick {
				return ns[a].Tick < ns[b].Tick
			}
			return ns[a].Pitch < ns[b].Pitch
		})
	}
	sort.SliceStable(s.Tempos, func(a, b int) bool { return s.Tempos[a].Tick < s.Tempos[b].Tick })
	sort.SliceStable(s.Meters, func(a, b int) bool { return s.Meters[a].Tick < s.Meters[b].Tick })
	if s.SecondsAt(s.EndTick()) > MaxSeconds {
		return ErrTooLong
	}
	return nil
}

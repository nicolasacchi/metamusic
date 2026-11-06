// Package hrtf holds the head-related impulse responses of the binaural operator (C221, C222): a compact measured table
// (SADIE II, one dummy-head subject, built offline by tools/hrtfgen) and the parametric alternative that needs no data.
// Everything here is deterministic: the table is int16 Q15 (exact scaling by 2^-15) and the parametric responses come from
// closed forms on this tree's own maths.
package hrtf

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// The table geometry (tools/hrtfgen writes the same numbers; TestHRTF_TableShape compares them with the header).
const (
	Rate    = 48000 // sample rate of the stored responses
	Taps    = 256
	AzStep  = 5    // degrees
	AzMin   = -180 // degrees, positive to the right
	AzCount = 73   // -180..180 inclusive
	magic   = "MMHRTF01"
)

// Elevations are the stored elevations in degrees.
var Elevations = [3]int{0, 15, 30}

//go:embed table.q15
var tableData []byte

// Ear selects the ear of a response.
type Ear int

// The two ears. The table stores the right ear only; the left ear of azimuth a is the right ear of -a (C221.2), which
// guarantees left/right symmetry.
const (
	Left Ear = iota
	Right
)

// Info is the provenance record of the table, from its header (C222.4). It is copied into every binaural recipe.
type Info struct {
	Dataset     string `json:"dataset"`
	Subject     string `json:"subject"`
	SubjectDesc string `json:"subject_desc"`
	Licence     string `json:"licence"`
	Credit      string `json:"credit"`
	// SHA12 is the first 12 hex digits of the sha256 of the whole table file.
	SHA12 string `json:"table_sha256_12"`
}

type header struct {
	Format     string `json:"format"`
	Info              // dataset, subject, licence, credit
	Rate       int    `json:"rate"`
	Taps       int    `json:"taps"`
	AzMin      int    `json:"az_min_deg"`
	AzStep     int    `json:"az_step_deg"`
	AzCount    int    `json:"az_count"`
	Elevations []int  `json:"elevations_deg"`
	Ear        string `json:"ear"`
	Files      int    `json:"source_files"`
}

// Table is the parsed measured table. It is immutable.
type Table struct {
	info Info
	q    [len(Elevations)][AzCount][Taps]int16
}

var def = mustParse(tableData)

func mustParse(b []byte) *Table {
	t, err := Parse(b)
	if err != nil {
		panic("hrtf: embedded table is invalid: " + err.Error())
	}
	return t
}

// Default is the committed table.
func Default() *Table { return def }

// Parse reads a table file: the magic, a little-endian header length, the JSON header, then int16 little-endian responses in
// elevation, azimuth, tap order.
func Parse(b []byte) (*Table, error) {
	if len(b) < len(magic)+4 || string(b[:len(magic)]) != magic {
		return nil, errors.New("not an mm-hrtf table")
	}
	hl := int(binary.LittleEndian.Uint32(b[len(magic):]))
	body := b[len(magic)+4:]
	if hl <= 0 || hl > len(body) {
		return nil, errors.New("bad header length")
	}
	var h header
	if err := json.Unmarshal(body[:hl], &h); err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	if h.Format != "mm-hrtf/1" || h.Rate != Rate || h.Taps != Taps || h.AzMin != AzMin || h.AzStep != AzStep || h.AzCount != AzCount ||
		len(h.Elevations) != len(Elevations) || h.Ear != "right" {
		return nil, errors.New("header does not match the table geometry")
	}
	for i, e := range h.Elevations {
		if e != Elevations[i] {
			return nil, errors.New("header elevations do not match")
		}
	}
	data := body[hl:]
	if len(data) != 2*len(Elevations)*AzCount*Taps {
		return nil, fmt.Errorf("data is %d bytes, want %d", len(data), 2*len(Elevations)*AzCount*Taps)
	}
	t := &Table{info: h.Info}
	sum := sha256.Sum256(b)
	t.info.SHA12 = hex.EncodeToString(sum[:])[:12]
	i := 0
	for e := range t.q {
		for a := range t.q[e] {
			for n := range t.q[e][a] {
				t.q[e][a][n] = int16(binary.LittleEndian.Uint16(data[i:])) //nolint:gosec // two's complement reinterpretation
				i += 2
			}
		}
	}
	return t, nil
}

// Info is the provenance of the table.
func (t *Table) Info() Info { return t.info }

// SnapAz is the table azimuth nearest to az (degrees, positive to the right), clamped to -180..180.
func SnapAz(az int) int {
	az = max(AzMin, min(-AzMin, az))
	return AzMin + (az-AzMin+AzStep/2)/AzStep*AzStep
}

// SnapEl is the stored elevation nearest to el, the lower one on a tie.
func SnapEl(el int) int {
	best := Elevations[0]
	for _, e := range Elevations {
		if abs(el-e) < abs(el-best) {
			best = e
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// IR returns the 256-tap response of ear for a source at (az, el), snapped to the table. The values are the Q15 integers
// scaled by 2^-15.
func (t *Table) IR(az, el int, ear Ear) []float64 {
	if ear == Left {
		az = -az
	}
	az = SnapAz(az)
	ei := 0
	for i, e := range Elevations {
		if e == SnapEl(el) {
			ei = i
		}
	}
	row := &t.q[ei][(az-AzMin)/AzStep]
	h := make([]float64, Taps)
	for n, v := range row {
		h[n] = float64(v) * 0x1p-15
	}
	return h
}

// IRAt is IR resampled once to rate by dsp.Resample (C221.2) when rate differs from 48 000 Hz; the common delay removed at
// build time is preserved, so interaural time differences stay put to within a sample.
func (t *Table) IRAt(az, el int, ear Ear, rate int, m *dsp.Meter) ([]float64, error) {
	h := t.IR(az, el, ear)
	if rate == Rate {
		return h, nil
	}
	y, err := dsp.ResampleMetered(h, float64(rate)/Rate, 0, m)
	if err != nil {
		return nil, err
	}
	if y == nil {
		return nil, fmt.Errorf("hrtf: cannot resample to %d Hz", rate)
	}
	return y, nil
}

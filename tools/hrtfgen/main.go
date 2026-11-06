//go:build ignore

// hrtfgen builds internal/dsp/hrtf/table.q15 from the SADIE II dummy-head HRIR WAV files (C222.4). It is run by hand, once,
// offline; it is not part of the build or of CI, and the raw dataset is never committed.
//
//	go run tools/hrtfgen/main.go -wav <dir of 48K_24bit WAVs> -archive <D1.zip> -out internal/dsp/hrtf/table.q15
//
// Steps: for every direction of the table (azimuth -180..180 in 5 degree steps, elevations 0, 15, 30) take the nearest measured
// response; remove ONE onset delay shared by all responses (interaural time differences survive); window the tail; equalise
// the diffuse field (inverse of the 1/3-octave-smoothed energy average over all directions, limited to +-12 dB, minimum phase);
// scale; quantise to int16 Q15; write the right-ear table with a header that records the sources. The left ear is the mirror.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// The table geometry; internal/dsp/hrtf/table.go repeats it and TestHRTF_TableShape compares the two.
const (
	rate     = 48000
	taps     = 256
	azStep   = 5
	azMin    = -180
	azCount  = 73
	fftN     = 1024
	mpN      = 4096
	onsetPre = 6    // samples kept before the earliest onset
	onsetThr = 0.10 // an onset is the first sample at 10 % of the response's peak
	fadeLen  = 32
	eqLimit  = 12.0 // dB
	peakQ15  = 0.95
)

var elevations = []int{0, 15, 30}

type header struct {
	Format        string  `json:"format"`
	Dataset       string  `json:"dataset"`
	Subject       string  `json:"subject"`
	SubjectDesc   string  `json:"subject_desc"`
	Licence       string  `json:"licence"`
	Credit        string  `json:"credit"`
	Rate          int     `json:"rate"`
	Taps          int     `json:"taps"`
	AzMin         int     `json:"az_min_deg"`
	AzStep        int     `json:"az_step_deg"`
	AzCount       int     `json:"az_count"`
	Elevations    []int   `json:"elevations_deg"`
	Ear           string  `json:"ear"`
	Archive       string  `json:"source_archive"`
	ArchiveSHA256 string  `json:"source_archive_sha256"`
	Files         int     `json:"source_files"`
	ListSHA256    string  `json:"source_list_sha256"`
	DelayRemoved  int     `json:"common_delay_removed_samples"`
	DFEQ          string  `json:"diffuse_field_eq"`
	Scale         float64 `json:"scale"`
}

type wavFile struct {
	name   string
	az, el float64 // as the dataset names them: azimuth anticlockwise (to the left), degrees
}

func main() {
	wavDir := flag.String("wav", "", "directory of the 48K_24bit HRIR WAV files")
	archive := flag.String("archive", "", "the dataset zip the WAVs came from (hashed into the header)")
	out := flag.String("out", "internal/dsp/hrtf/table.q15", "output table")
	flag.Parse()
	if *wavDir == "" || *archive == "" {
		fatal("need -wav and -archive")
	}
	files := listWAVs(*wavDir)

	// 1. the nearest measured response for every table direction
	type dir struct {
		az, el int
		file   wavFile
		h      []float64 // right ear
	}
	var dirs []dir
	var list []string
	var minOnset = taps
	for ei := range elevations {
		for ai := 0; ai < azCount; ai++ {
			az := azMin + ai*azStep
			f := nearest(files, float64(-az), float64(elevations[ei])) // ours is clockwise, the dataset's anticlockwise
			l, r := readWAV(filepath.Join(*wavDir, f.name))
			sum := sha256.Sum256(mustRead(filepath.Join(*wavDir, f.name)))
			list = append(list, fmt.Sprintf("%s\t%s", f.name, hex.EncodeToString(sum[:])))
			minOnset = min(minOnset, onset(l), onset(r))
			dirs = append(dirs, dir{az: az, el: elevations[ei], file: f, h: r})
		}
	}

	// 2. one common delay, then a tail fade
	delay := max(minOnset-onsetPre, 0)
	hs := make([][]float64, len(dirs))
	for i, d := range dirs {
		h := make([]float64, taps)
		copy(h, d.h[delay:])
		fade(h)
		hs[i] = h
	}

	// 3. diffuse-field equalisation
	eq, eqDesc := diffuseEQ(hs)
	for i, h := range hs {
		y := make([]float64, taps)
		for n := range y {
			var acc float64
			for k := 0; k <= n && k < len(eq); k++ {
				acc += float64(eq[k] * h[n-k])
			}
			y[n] = acc
		}
		fade(y)
		hs[i] = y
	}

	// 4. scale to a fixed peak, quantise
	var peak float64
	for _, h := range hs {
		for _, v := range h {
			peak = math.Max(peak, math.Abs(v))
		}
	}
	scale := peakQ15 / peak
	var data bytes.Buffer
	for _, h := range hs {
		for _, v := range h {
			q := int16(math.Round(float64(v*scale) * 32768))
			_ = binary.Write(&data, binary.LittleEndian, q)
		}
	}

	// 5. header and file
	arch := mustRead(*archive)
	asum := sha256.Sum256(arch)
	lsum := sha256.Sum256([]byte(strings.Join(list, "\n") + "\n"))
	hd := header{
		Format: "mm-hrtf/1", Dataset: "SADIE II", Subject: "D1", SubjectDesc: "Neumann KU100 dummy head, 48 kHz 24 bit, 256 taps",
		Licence: "Apache-2.0",
		Credit:  "SADIE II database (subject D1, Neumann KU100), (c) 2018 University of York, Apache-2.0; Armstrong, Thresh and Kearney, The Audio Lab, University of York; doi:10.3390/app8112029. Modified: onset-aligned, diffuse-field equalised, truncated to 256 taps, 16-bit.",
		Rate:    rate, Taps: taps, AzMin: azMin, AzStep: azStep, AzCount: azCount, Elevations: elevations, Ear: "right",
		Archive: "D1.zip (Zenodo 10.5281/zenodo.12092466, v2-2)", ArchiveSHA256: hex.EncodeToString(asum[:]),
		Files: len(list), ListSHA256: hex.EncodeToString(lsum[:]), DelayRemoved: delay, DFEQ: eqDesc, Scale: scale,
	}
	hj, err := json.Marshal(hd)
	if err != nil {
		fatal(err.Error())
	}
	var file bytes.Buffer
	file.WriteString("MMHRTF01")
	_ = binary.Write(&file, binary.LittleEndian, uint32(len(hj)))
	file.Write(hj)
	file.Write(data.Bytes())
	if err := os.WriteFile(*out, file.Bytes(), 0o644); err != nil {
		fatal(err.Error())
	}
	tsum := sha256.Sum256(file.Bytes())
	fmt.Printf("wrote %s: %d bytes, sha256 %s\ncommon delay removed: %d samples (earliest onset %d)\npeak before scaling %.4f, scale %.4f\n%s\n",
		*out, file.Len(), hex.EncodeToString(tsum[:]), delay, minOnset, peak, scale, eqDesc)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "hrtfgen:", msg)
	os.Exit(1)
}

func mustRead(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		fatal(err.Error())
	}
	return b
}

// listWAVs parses the dataset's file names, "azi_<az>_ele_<el>.wav" with a decimal comma.
func listWAVs(dir string) []wavFile {
	ents, err := os.ReadDir(dir)
	if err != nil {
		fatal(err.Error())
	}
	var out []wavFile
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "azi_") || !strings.HasSuffix(n, ".wav") {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(n, "azi_"), ".wav"), "_ele_")
		if len(parts) != 2 {
			continue
		}
		az, e1 := strconv.ParseFloat(strings.Replace(parts[0], ",", ".", 1), 64)
		el, e2 := strconv.ParseFloat(strings.Replace(parts[1], ",", ".", 1), 64)
		if e1 != nil || e2 != nil {
			continue
		}
		out = append(out, wavFile{name: n, az: az, el: el})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// nearest picks the file at the elevation nearest el and, within it, the azimuth nearest az (circular distance).
func nearest(files []wavFile, az, el float64) wavFile {
	bestEl := math.Inf(1)
	for _, f := range files {
		bestEl = math.Min(bestEl, math.Abs(f.el-el))
	}
	var best wavFile
	bestD := math.Inf(1)
	for _, f := range files {
		if math.Abs(f.el-el) > bestEl+1e-9 {
			continue
		}
		d := math.Mod(math.Abs(f.az-az), 360)
		d = math.Min(d, 360-d)
		if d < bestD-1e-9 {
			best, bestD = f, d
		}
	}
	return best
}

// readWAV reads a 24-bit PCM stereo WAV into two float64 channels in [-1, 1).
func readWAV(path string) (l, r []float64) {
	b := mustRead(path)
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		fatal(path + ": not a RIFF/WAVE file")
	}
	var data []byte
	for i := 12; i+8 <= len(b); {
		id, sz := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:i+8]))
		body := b[i+8 : min(i+8+sz, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 || binary.LittleEndian.Uint16(body[0:]) != 1 || binary.LittleEndian.Uint16(body[2:]) != 2 ||
				binary.LittleEndian.Uint32(body[4:]) != rate || binary.LittleEndian.Uint16(body[14:]) != 24 {
				fatal(path + ": expected 48 kHz 24-bit stereo PCM")
			}
		case "data":
			data = body
		}
		i += 8 + sz + sz&1
	}
	n := len(data) / 6
	if n < taps {
		fatal(path + ": fewer than 256 frames")
	}
	l, r = make([]float64, n), make([]float64, n)
	for k := 0; k < n; k++ {
		l[k] = pcm24(data[k*6:]) / 8388608
		r[k] = pcm24(data[k*6+3:]) / 8388608
	}
	return l[:taps], r[:taps]
}

func pcm24(b []byte) float64 {
	v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
	if v&0x800000 != 0 {
		v |= ^0xffffff
	}
	return float64(v)
}

func onset(h []float64) int {
	var peak float64
	for _, v := range h {
		peak = math.Max(peak, math.Abs(v))
	}
	for i, v := range h {
		if math.Abs(v) >= onsetThr*peak {
			return i
		}
	}
	return 0
}

// fade applies a raised-cosine fall over the last fadeLen taps.
func fade(h []float64) {
	for i := 0; i < fadeLen; i++ {
		h[len(h)-fadeLen+i] *= 0.5 * (1 + math.Cos(math.Pi*(float64(i)+0.5)/fadeLen))
	}
}

// diffuseEQ returns a minimum-phase FIR whose magnitude is the inverse of the energy average over all responses, smoothed to
// 1/3 octave and limited to +-12 dB, and a one-line description for the header.
func diffuseEQ(hs [][]float64) ([]float64, string) {
	bins := fftN/2 + 1
	p := make([]float64, bins)
	for _, h := range hs {
		re, im := dsp.RFFT(h, fftN)
		for k := range p {
			p[k] += float64(re[k]*re[k]) + float64(im[k]*im[k])
		}
	}
	for k := range p {
		p[k] /= float64(len(hs))
	}
	const sixth = 1.122462048309373 // 2^(1/6)
	sm := make([]float64, bins)
	for k := 1; k < bins; k++ {
		lo, hi := int(math.Ceil(float64(k)/sixth)), int(math.Floor(float64(k)*sixth))
		lo, hi = max(lo, 1), min(hi, bins-1)
		var s float64
		for j := lo; j <= hi; j++ {
			s += p[j]
		}
		sm[k] = s / float64(hi-lo+1)
	}
	sm[0] = sm[1]
	// level reference: the mean of the smoothed spectrum between 200 Hz and 10 kHz
	k0, k1 := int(200*fftN/rate), int(10000*fftN/rate)
	var ref float64
	for k := k0; k <= k1; k++ {
		ref += sm[k]
	}
	ref /= float64(k1 - k0 + 1)
	logG := make([]float64, bins) // natural log of the amplitude gain
	var gmin, gmax float64
	for k := range logG {
		db := math.Max(-eqLimit, math.Min(eqLimit, 10*dsp.Log(ref/sm[k])/math.Ln10))
		gmin, gmax = math.Min(gmin, db), math.Max(gmax, db)
		logG[k] = db / 20 * math.Ln10
	}
	// minimum phase by the real cepstrum on a finer grid (linear interpolation of the log gain)
	re, im := make([]float64, mpN), make([]float64, mpN)
	for k := 0; k <= mpN/2; k++ {
		x := float64(k) * float64(fftN) / float64(mpN)
		i := min(int(x), bins-2)
		f := x - float64(i)
		v := logG[i] + float64(f*(logG[i+1]-logG[i]))
		re[k] = v
		if k > 0 && k < mpN/2 {
			re[mpN-k] = v
		}
	}
	dsp.IFFT(re, im) // real cepstrum
	for n := 1; n < mpN/2; n++ {
		re[n] *= 2
		re[mpN-n], im[mpN-n] = 0, 0
	}
	for n := range im {
		im[n] = 0
	}
	dsp.FFT(re, im)
	for k := range re {
		m := dsp.Exp(re[k])
		re[k], im[k] = float64(m*math.Cos(im[k])), float64(m*math.Sin(im[k]))
	}
	dsp.IFFT(re, im)
	g := re[:taps]
	return g, fmt.Sprintf("min-phase inverse of the 1/3-octave energy average of %d responses, gain %.1f..%.1f dB, level ref 200 Hz-10 kHz", len(hs), gmin, gmax)
}

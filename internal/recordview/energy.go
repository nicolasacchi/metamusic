package recordview

import "math"

// The energy curve (C124.3, D-L2-11).
const (
	energyPoints = 120
	energyFloor  = -70.0
)

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }

// curve is a take's stored short-term series after the plan gain: floor -70.0, one decimal, bucket-mean downsampled to at
// most energyPoints values (bucket = ceil(len / energyPoints)).
func curve(series []float64, gain float64) []float64 {
	out := []float64{}
	if len(series) == 0 {
		return out
	}
	bucket := (len(series) + energyPoints - 1) / energyPoints
	for start := 0; start < len(series); start += bucket {
		end := min(start+bucket, len(series))
		var sum float64
		for _, v := range series[start:end] {
			x := energyFloor
			if v > energyFloor {
				x = max(round1(v+gain), energyFloor)
			}
			sum += x
		}
		out = append(out, round1(sum/float64(end-start)))
	}
	return out
}

// energy builds the energy-curve preview of a Record.
func energy(d *data) *Energy {
	target, gains := planGains(d)
	e := &Energy{TargetLUFS: target, Slots: []EnergySlot{}}
	for _, r := range d.rs {
		es := EnergySlot{SlotID: r.Slot.ID, LUFS: []float64{}}
		if b, ok := d.briefs[r.Slot.SongID]; ok {
			es.BPM = b.BPM
		}
		if playable(r) {
			es.DurationS = r.Take.DurationS
			e.TotalDurationS += r.Take.DurationS
			g, ok := gains[r.Slot.ID]
			es.GainDB = gainDB(g, ok)
			l := d.loud[r.Take.ID]
			gain := 0.0
			if es.GainDB != nil {
				gain = *es.GainDB
			}
			if l.Measure.OK {
				m := round2(l.Measure.LUFS + gain)
				es.MeanLUFS = &m
			}
			es.LUFS = curve(l.ShortTerm1Hz, gain)
		}
		e.Slots = append(e.Slots, es)
	}
	return e
}

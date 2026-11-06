package pv

// Peak is a spectral peak with its region of influence: the bins Lo..Hi (inclusive) that follow the peak's phase.
type Peak struct{ Bin, Lo, Hi int }

// PeakFloor is the level, relative to the strongest bin of the frame, below which a local maximum is not a peak (-80 dB).
const PeakFloor = 1e-4

// FindPeaks finds the peaks of a magnitude spectrum and their regions of influence (Laroche-Dolson). A peak is a bin that
// is above floor*max, strictly above its two left neighbours and at least as high as its two right ones (so a plateau gives
// one peak). Consecutive regions are split at the amplitude minimum between their peaks (the lowest bin, first on ties,
// belongs to the left region); the first region starts at bin 0 and the last ends at the last bin, so the regions tile the
// whole spectrum. A silent spectrum has no peaks. dst is reused when it has the capacity.
func FindPeaks(mag []float64, floor float64, dst []Peak) []Peak {
	dst = dst[:0]
	var top float64
	for _, m := range mag {
		if m > top {
			top = m
		}
	}
	if top == 0 {
		return dst
	}
	thr := float64(floor * top)
	for k := range mag {
		if mag[k] <= thr {
			continue
		}
		ok := true
		for d := 1; d <= 2 && ok; d++ {
			if k-d >= 0 && mag[k] <= mag[k-d] {
				ok = false
			}
			if k+d < len(mag) && mag[k] < mag[k+d] {
				ok = false
			}
		}
		if ok {
			dst = append(dst, Peak{Bin: k})
		}
	}
	for i := range dst {
		if i == 0 {
			dst[i].Lo = 0
		}
		if i == len(dst)-1 {
			dst[i].Hi = len(mag) - 1
			continue
		}
		lo, hi := dst[i].Bin, dst[i+1].Bin
		low := lo + 1
		for k := lo + 1; k < hi; k++ {
			if mag[k] < mag[low] {
				low = k
			}
		}
		dst[i].Hi = low
		dst[i+1].Lo = low + 1
	}
	return dst
}

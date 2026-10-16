package cover

// splitmix64 is the 10-line generator of the cover algorithm (C110.1); math/rand is deliberately not used so the
// output cannot change with a Go release.
type splitmix64 struct{ s uint64 }

func (r *splitmix64) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// unit is the next value as a fraction in [0, 1).
func (r *splitmix64) unit() float64 { return float64(r.next()>>11) / (1 << 53) }

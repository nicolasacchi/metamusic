package sequencer

// segCost is the part of J that depends on positions a..b of an order: their shape terms and the transitions that
// touch them (including the two boundary transitions).
func (pr *problem) segCost(o []int, a, b int) int64 {
	var j int64
	for p := a; p <= b; p++ {
		j += pr.shape[o[p]][p]
	}
	for p := max(a-1, 0); p <= b && p+1 < len(o); p++ {
		j += pr.trans[o[p]][o[p+1]]
	}
	return j
}

// localSearch improves an order in place (returned for convenience): 2-opt reversals, then single-song moves, scanned
// in a fixed order with first improvement, for at most MaxLocalPasses passes. The endpoints never move. A candidate
// is compared with the current order over the span it changes only; tmp mirrors order between candidates.
func (pr *problem) localSearch(order []int) []int {
	n := len(order)
	lo, hi := 0, n-1
	if pr.first >= 0 {
		lo = 1
	}
	if pr.last >= 0 {
		hi = n - 2
	}
	tmp := append([]int(nil), order...)
	// try evaluates tmp (already changed over a..b) against order and keeps it when strictly better, else restores.
	try := func(a, b int) bool {
		if pr.segCost(tmp, a, b) < pr.segCost(order, a, b) {
			copy(order[a:b+1], tmp[a:b+1])
			return true
		}
		copy(tmp[a:b+1], order[a:b+1])
		return false
	}
	for pass := 0; pass < MaxLocalPasses; pass++ {
		improved := false
		for a := lo; a < hi; a++ {
			for b := a + 1; b <= hi; b++ {
				for x, y := a, b; x < y; x, y = x+1, y-1 {
					tmp[x], tmp[y] = tmp[y], tmp[x]
				}
				if try(a, b) {
					improved = true
				}
			}
		}
		for i := lo; i <= hi; i++ {
			for j := lo; j <= hi; j++ {
				if i == j {
					continue
				}
				a, b := min(i, j), max(i, j)
				v := tmp[i]
				if i < j {
					copy(tmp[i:j], tmp[i+1:j+1])
				} else {
					copy(tmp[j+1:i+1], tmp[j:i])
				}
				tmp[j] = v
				if try(a, b) {
					improved = true
				}
			}
		}
		if !improved {
			break
		}
	}
	return order
}

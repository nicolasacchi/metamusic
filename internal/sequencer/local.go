package sequencer

// localSearch improves an order in place (returned for convenience): 2-opt reversals, then single-song moves, scanned
// in a fixed order with first improvement, for at most MaxLocalPasses passes. The endpoints never move.
func (pr *problem) localSearch(order []int) []int {
	n := len(order)
	lo, hi := 0, n-1
	if pr.first >= 0 {
		lo = 1
	}
	if pr.last >= 0 {
		hi = n - 2
	}
	cur := pr.cost(order)
	tmp := make([]int, n)
	for pass := 0; pass < MaxLocalPasses; pass++ {
		improved := false
		for a := lo; a < hi; a++ {
			for b := a + 1; b <= hi; b++ {
				copy(tmp, order)
				for x, y := a, b; x < y; x, y = x+1, y-1 {
					tmp[x], tmp[y] = tmp[y], tmp[x]
				}
				if c := pr.cost(tmp); c < cur {
					copy(order, tmp)
					cur, improved = c, true
				}
			}
		}
		for i := lo; i <= hi; i++ {
			for j := lo; j <= hi; j++ {
				if i == j {
					continue
				}
				copy(tmp, order)
				v := tmp[i]
				if i < j {
					copy(tmp[i:j], tmp[i+1:j+1])
				} else {
					copy(tmp[j+1:i+1], tmp[j:i])
				}
				tmp[j] = v
				if c := pr.cost(tmp); c < cur {
					copy(order, tmp)
					cur, improved = c, true
				}
			}
		}
		if !improved {
			break
		}
	}
	return order
}

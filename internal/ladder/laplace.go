package ladder

import "math"

// laplaceSD fills sd (indexed by item) for items with comparisons from the inverse
// Hessian of -LP at the MAP, with nu held at its estimate. Items without comparisons keep
// the prior SD already stored in sd. nu's own uncertainty is ignored.
func laplaceSD(p *problem, theta []float64, nu, priorSD float64, sd []float64) {
	pos := make([]int, p.n)
	var items []int
	for i := 0; i < p.n; i++ {
		pos[i] = -1
		if p.has[i] {
			pos[i] = len(items)
			items = append(items, i)
		}
	}
	m := len(items)
	if m == 0 {
		return
	}
	h := make([]float64, m*m)
	prior := 1 / (priorSD * priorSD)
	for k := 0; k < m; k++ {
		h[k*m+k] = prior
	}
	for _, pr := range p.pairs {
		n := pr.n()
		a, b := math.Exp(theta[pr.i]), math.Exp(theta[pr.j])
		t := nu * math.Sqrt(a*b)
		d := a + b + t
		pi, pj, pt := a/d, b/d, t/d
		qi, qj := pi+pt/2, pj+pt/2
		x, y := pos[pr.i], pos[pr.j]
		h[x*m+x] += n * (pi + pt/4 - qi*qi)
		h[y*m+y] += n * (pj + pt/4 - qj*qj)
		off := n * (pt/4 - qi*qj)
		h[x*m+y] += off
		h[y*m+x] += off
	}
	for k, i := range items {
		s := 1 / (1 + math.Exp(-theta[i]))
		h[k*m+k] += p.nAnch[i] * s * (1 - s)
	}
	// Cholesky H = L L^T (lower, in place).
	for j := 0; j < m; j++ {
		for k := 0; k < j; k++ {
			v := h[j*m+k]
			for l := 0; l < k; l++ {
				v -= h[j*m+l] * h[k*m+l]
			}
			h[j*m+k] = v / h[k*m+k]
		}
		v := h[j*m+j]
		for l := 0; l < j; l++ {
			v -= h[j*m+l] * h[j*m+l]
		}
		h[j*m+j] = math.Sqrt(v)
	}
	// (H^-1)_ii = sum_k (L^-1)_ki^2; build L^-1 column by column (forward substitution).
	acc := make([]float64, m)
	col := make([]float64, m)
	for c := 0; c < m; c++ {
		for r := c; r < m; r++ {
			v := 0.0
			if r == c {
				v = 1
			}
			for l := c; l < r; l++ {
				v -= h[r*m+l] * col[l]
			}
			col[r] = v / h[r*m+r]
			acc[c] += col[r] * col[r]
		}
	}
	for k, i := range items {
		sd[i] = math.Sqrt(acc[k])
	}
}

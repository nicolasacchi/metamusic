package align

// KeyShift is the number of semitones to move key B so that it lands in key A (C204.4). A minor key and its relative major
// are the same pitch set, so eq(pc, minor) = (pc + 3*minor) mod 12 and the shift is (eq(a) - eq(b)) mod 12 folded into
// [-6, +5] (a tritone folds down: 6 becomes -6). Pitch classes outside 0..11 are reduced modulo 12.
func KeyShift(aPC int, aMinor bool, bPC int, bMinor bool) float64 {
	d := mod12(eq(aPC, aMinor) - eq(bPC, bMinor))
	if d > 5 {
		d -= 12
	}
	return float64(d)
}

func eq(pc int, minor bool) int {
	if minor {
		pc += 3
	}
	return mod12(pc)
}

func mod12(n int) int { return ((n % 12) + 12) % 12 }

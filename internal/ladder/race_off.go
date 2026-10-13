//go:build !race

package ladder

// raceEnabled reports whether the race detector is compiled in (timing tests skip).
const raceEnabled = false

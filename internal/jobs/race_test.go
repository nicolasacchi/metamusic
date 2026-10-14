//go:build race

package jobs

// raceEnabled: the sweeps that fail every database call use a stride under the race detector, which
// slows each rig down several-fold; the full sweep runs in plain `go test`.
const raceEnabled = true

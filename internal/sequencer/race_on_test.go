//go:build race

package sequencer

// raceOn trims the instance counts of the heavy property tests: the search is the same code, only slower.
const raceOn = true

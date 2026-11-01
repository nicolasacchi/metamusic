package features

// Lane FC1 replaces these pure functions and deletes this file. The signatures are frozen by L3-F2.

// CamelotOf maps a tonic pitch class (C = 0) and mode to the Camelot code (C135.1).
func CamelotOf(tonicPC int, mode string) Camelot { return Camelot{} }

// String renders the code, for example "8A".
func (c Camelot) String() string { return "" }

// KeyName renders the committed spelling, for example "A minor" (C135.1).
func KeyName(tonicPC int, mode string) string { return "" }

// ParseStyleKey reads a key from a style string (C135.3).
func ParseStyleKey(style string) (tonicPC int, mode string, ok bool) { return 0, "", false }

// ParseABCKey reads the K: header of an ABC score (C135.3).
func ParseABCKey(abc string) (tonicPC int, mode string, ok bool) { return 0, "", false }

// PredictEnergy is the facet prior, energy x 10 000 (C135.4).
func PredictEnergy(f Facets) (x10000 int, ok bool) { return 0, false }

// DeriveTake is the pure, deterministic derivation of one take's row (C135.2).
func DeriveTake(in TakeInput) Row { return Row{} }

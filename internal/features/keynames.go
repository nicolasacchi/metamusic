package features

// Committed spelling tables of KeyName (C135.1), indexed by pitch class (C = 0).
var (
	majorNames = [12]string{"C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"}
	minorNames = [12]string{"C", "C#", "D", "Eb", "E", "F", "F#", "G", "G#", "A", "Bb", "B"}
)

// KeyName renders the committed spelling, for example "A minor" (C135.1). An unknown mode gives "".
func KeyName(tonicPC int, mode string) string {
	pc := ((tonicPC % 12) + 12) % 12
	switch mode {
	case "major":
		return majorNames[pc] + " major"
	case "minor":
		return minorNames[pc] + " minor"
	}
	return ""
}

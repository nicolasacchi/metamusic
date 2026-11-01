package features

import "strconv"

// majorCamelot is the Camelot number of each major key by tonic pitch class (C135.1): C 8B, Db 3B, D 10B, Eb 5B,
// E 12B, F 7B, F# 2B, G 9B, Ab 4B, A 11B, Bb 6B, B 1B. A minor key shares the number of its relative major.
var majorCamelot = [12]int{8, 3, 10, 5, 12, 7, 2, 9, 4, 11, 6, 1}

// CamelotOf maps a tonic pitch class (C = 0) and mode ("major" | "minor") to the Camelot code (C135.1).
// An unknown mode gives the zero Camelot.
func CamelotOf(tonicPC int, mode string) Camelot {
	pc := ((tonicPC % 12) + 12) % 12
	switch mode {
	case "major":
		return Camelot{Num: majorCamelot[pc], Letter: "B"}
	case "minor":
		return Camelot{Num: majorCamelot[(pc+3)%12], Letter: "A"}
	}
	return Camelot{}
}

// String renders the code, for example "8A"; the zero value renders "".
func (c Camelot) String() string {
	if c.Num == 0 {
		return ""
	}
	return strconv.Itoa(c.Num) + c.Letter
}

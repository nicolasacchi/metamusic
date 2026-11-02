package songview

import "strconv"

// CamelotNameForTest is a test seam over camelotName for a code such as "8A".
func CamelotNameForTest(code string) string {
	n, _ := strconv.Atoi(code[:len(code)-1])
	return camelotName(n, code[len(code)-1:])
}

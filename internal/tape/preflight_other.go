//go:build !unix

package tape

// freeBytes cannot tell on this platform; the preflight is skipped.
func freeBytes(string) (uint64, error) { return 0, errFreeUnknown }

package fake

// ResetSimKinds empties the process-wide stage table (tests of RegisterSimKind only).
func ResetSimKinds() {
	simKindMu.Lock()
	defer simKindMu.Unlock()
	simKinds = map[string]SimKindHook{}
}

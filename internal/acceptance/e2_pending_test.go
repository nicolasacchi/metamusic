package acceptance_test

// e2PendingStub lists the E2 operations whose handler still answers 501 (lane E2-OP has not landed): the script keeps their
// entry, so the registry-derived set stays complete, but does not call them. E2-OP deletes the entry when it implements the
// operation.
var e2PendingStub = map[string]bool{"list_engines": true}

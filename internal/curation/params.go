// Package curation holds the two plain privacy opt-ins of the curation axis and their stats (C127, D-L2-16). It imports only
// store and auth, so the catalog, tape and recordview can all read the flags without reaching Muse text.
package curation

// The settings keys (C118, C127.1). Absent means false.
const (
	KeyExport    = "curation.export_muse_names"
	KeySubsonic  = "curation.subsonic_muse_names"
	KeyUpdatedAt = "curation.updated_at"
	KeyUpdatedBy = "curation.updated_by"
)

// Keys lists every settings key under curation. (Verify R9 asserts that no other key exists there.)
var Keys = []string{KeyExport, KeySubsonic, KeyUpdatedAt, KeyUpdatedBy}

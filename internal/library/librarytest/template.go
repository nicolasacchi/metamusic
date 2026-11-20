package librarytest

import "github.com/nicolasacchi/metamusic/internal/store"

// Every test that builds a library through this package opens a brand-new store, and applying the migrations from zero costs
// 13-16 s per database under -race (internal/store TestMigrate_ImageEqualsFromZero pins that the seeded image is the same
// schema). Importing the fixtures therefore opts the importing test binary into the per-process migrated template, so a package
// that forgets its own TestMain (internal/provenance did: its tests "hung" for 20 minutes in store.Open) cannot pay it per test.
// librarytest is test-support code, listed in cmd/metamusic's nohelpers_test so it never reaches the binary.
func init() { store.UseMigratedTemplateForTests() }

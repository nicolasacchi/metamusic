package acceptance_test

import (
	"github.com/nicolasacchi/metamusic/internal/store"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
)

// TestMain removes the shared synthetic archive templates after the run. Tests
// import the archive through archivetest.SyntheticArchive (once per binary) and
// copy it per rig with servertest.Options.Archive.
func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	code := m.Run()
	archivetest.Cleanup()
	os.Exit(code)
}

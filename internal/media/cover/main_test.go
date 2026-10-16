package cover_test

import (
	"flag"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

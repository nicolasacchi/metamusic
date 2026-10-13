package auth

import (
	"github.com/nicolasacchi/metamusic/internal/store"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	UseCheapHashForTests()
	os.Exit(m.Run())
}

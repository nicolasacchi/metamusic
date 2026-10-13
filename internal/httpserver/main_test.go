package httpserver_test

import (
	"github.com/nicolasacchi/metamusic/internal/store"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	auth.UseCheapHashForTests()
	os.Exit(m.Run())
}

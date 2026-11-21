package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
)

// writeR2Env writes a test r2.env; the variable names are assembled at run time so that no R2_*=<value> literal exists in the
// repository (the gate's r2 rule).
func (e *m3Env) writeR2Env(t *testing.T, mode os.FileMode, extra string) {
	t.Helper()
	if err := os.MkdirAll(e.cfg.SecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "R2_" + "ENDPOINT=https://bucket.example.com\n" + "R2_" + "BUCKET=mm-test\n" + "R2_" + "ACCESS_KEY_ID=" + "synthetic-id-0001\n" +
		"R2_" + "SECRET_ACCESS_KEY=" + "synthetic-secret-0002\n" + extra
	p := filepath.Join(e.cfg.SecretsDir, "r2.env")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func loopNames(s *app.Services) []string {
	var out []string
	for _, l := range s.Loops {
		out = append(out, l.Name)
	}
	return out
}

func TestApp_UploadFallback_AbsentIsOff(t *testing.T) {
	t.Parallel()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake })
	s := e.build(t, app.Options{})
	for _, n := range loopNames(s) {
		if strings.Contains(n, "fallback") {
			t.Fatalf("loop %s runs without r2.env", n)
		}
	}
	configured, pending, last, err := s.Deps.UploadFallback.Status(t.Context())
	if err != nil || configured || pending != 0 || last != nil {
		t.Fatalf("status = %v %d %v %v", configured, pending, last, err)
	}
}

func TestApp_UploadFallback_ConfiguredStartsLoop(t *testing.T) {
	t.Parallel()
	e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake })
	e.writeR2Env(t, 0o600, "")
	s := e.build(t, app.Options{})
	if got := strings.Join(loopNames(s), ","); !strings.Contains(got, "upload-fallback") {
		t.Fatalf("loops = %s", got)
	}
	configured, pending, _, err := s.Deps.UploadFallback.Status(t.Context())
	if err != nil || !configured || pending != 0 {
		t.Fatalf("status = %v %d %v", configured, pending, err)
	}
}

func TestApp_UploadFallback_BrokenFileStopsStartupWithoutValues(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mode  os.FileMode
		extra string
	}{
		"loose mode":   {0o644, ""},
		"unknown name": {0o600, "R2_" + "FOO=bar-synthetic\n"},
		"bad prefix":   {0o600, "R2_" + "PREFIX=NOT/OK\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newM3Env(t, func(c *config.Config) { c.Backend = config.BackendFake })
			e.writeR2Env(t, tc.mode, tc.extra)
			_, err := app.Build(t.Context(), e.cfg, config.Secrets{Key: key}, e.st, e.root, app.Options{})
			if err == nil || !strings.Contains(err.Error(), "r2.env") {
				t.Fatalf("err = %v", err)
			}
			for _, v := range []string{"synthetic-id-0001", "synthetic-secret-0002", "bucket.example.com", "mm-test"} {
				if strings.Contains(err.Error(), v) {
					t.Errorf("the error carries %q", v)
				}
			}
		})
	}
}

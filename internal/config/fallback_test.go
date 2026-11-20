package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeR2(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "r2.env")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

// r2Lines builds the file at run time so no assignment literal sits in the repo.
func r2Lines(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString("R2_" + kv[i] + "=" + kv[i+1] + "\n")
	}
	return b.String()
}

const (
	fbKeyID  = "AKIA" + "MMTESTFIXTURE01"
	fbSecret = "mmtestsecret" + "fixture0123456789abcdefghijklmnop"
)

func goodR2() string {
	return r2Lines("ENDPOINT", "https://mm-r2.example.com", "BUCKET", "mm-test", "ACCESS_KEY_ID", fbKeyID, "SECRET_ACCESS_KEY", fbSecret)
}

func TestUploadFallbackConfig_Table(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		got, ok, err := LoadUploadFallback(t.TempDir())
		if err != nil || ok || got != (UploadFallback{}) {
			t.Fatalf("absent: %v %v %v", got, ok, err)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		got, ok, err := LoadUploadFallback(writeR2(t, goodR2(), 0o600))
		if err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if got.Region != "auto" || got.Prefix != "mm/" || got.Bucket != "mm-test" || got.Endpoint != "https://mm-r2.example.com" ||
			got.AccessKeyID != fbKeyID || got.SecretAccessKey != fbSecret {
			t.Fatalf("got %+v", got.String())
		}
	})
	t.Run("overrides and 0400", func(t *testing.T) {
		got, ok, err := LoadUploadFallback(writeR2(t, goodR2()+r2Lines("REGION", "eu-west-1", "PREFIX", "ab-1/"), 0o400))
		if err != nil || !ok || got.Region != "eu-west-1" || got.Prefix != "ab-1/" {
			t.Fatalf("%v %v %v", got.String(), ok, err)
		}
	})
	t.Run("String", func(t *testing.T) {
		got, _, _ := LoadUploadFallback(writeR2(t, goodR2(), 0o600))
		want := "UploadFallback{endpoint:set bucket:set keys:set}"
		for _, s := range []string{got.String(), (UploadFallback{}).String()} {
			if strings.Contains(s, fbSecret) || strings.Contains(s, fbKeyID) {
				t.Fatalf("leak %s", s)
			}
		}
		if got.String() != want || got.GoString() != want {
			t.Fatalf("String = %s", got.String())
		}
		if s := fmtAll(got); strings.Contains(s, fbSecret) || strings.Contains(s, fbKeyID) {
			t.Fatalf("fmt leak %s", s)
		}
	})
	for _, tc := range []struct {
		name, body, wantVar string
		mode                os.FileMode
	}{
		{"missing endpoint", r2Lines("BUCKET", "mm-test", "ACCESS_KEY_ID", fbKeyID, "SECRET_ACCESS_KEY", fbSecret), "R2_ENDPOINT", 0o600},
		{"missing bucket", r2Lines("ENDPOINT", "https://mm-r2.example.com", "ACCESS_KEY_ID", fbKeyID, "SECRET_ACCESS_KEY", fbSecret), "R2_BUCKET", 0o600},
		{"missing key id", r2Lines("ENDPOINT", "https://mm-r2.example.com", "BUCKET", "mm-test", "SECRET_ACCESS_KEY", fbSecret), "R2_ACCESS_KEY_ID", 0o600},
		{"missing secret", r2Lines("ENDPOINT", "https://mm-r2.example.com", "BUCKET", "mm-test", "ACCESS_KEY_ID", fbKeyID), "R2_SECRET_ACCESS_KEY", 0o600},
		{"http endpoint", strings.Replace(goodR2(), "https://", "http://", 1), "R2_ENDPOINT", 0o600},
		{"endpoint path", strings.Replace(goodR2(), "example.com", "example.com/v1", 1), "R2_ENDPOINT", 0o600},
		{"endpoint query", strings.Replace(goodR2(), "example.com", "example.com?x=1", 1), "R2_ENDPOINT", 0o600},
		{"endpoint userinfo", strings.Replace(goodR2(), "https://", "https://u:p@", 1), "R2_ENDPOINT", 0o600},
		{"bucket upper", strings.Replace(goodR2(), "mm-test", "MM-Test", 1), "R2_BUCKET", 0o600},
		{"bucket short", strings.Replace(goodR2(), "mm-test", "ab", 1), "R2_BUCKET", 0o600},
		{"bucket dash end", strings.Replace(goodR2(), "mm-test", "mm-test-", 1), "R2_BUCKET", 0o600},
		{"prefix no slash", goodR2() + r2Lines("PREFIX", "mm"), "R2_PREFIX", 0o600},
		{"prefix upper", goodR2() + r2Lines("PREFIX", "MM/"), "R2_PREFIX", 0o600},
		{"prefix long", goodR2() + r2Lines("PREFIX", strings.Repeat("a", 17)+"/"), "R2_PREFIX", 0o600},
		{"prefix nested", goodR2() + r2Lines("PREFIX", "a/b/"), "R2_PREFIX", 0o600},
		{"group readable", goodR2(), "r2.env", 0o640},
		{"world readable", goodR2(), "r2.env", 0o644},
		{"unknown var", goodR2() + r2Lines("ENDPOINT_URL", "x"), "R2_ENDPOINT_URL", 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := LoadUploadFallback(writeR2(t, tc.body, tc.mode))
			if err == nil || ok {
				t.Fatalf("want error, got ok=%v err=%v", ok, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantVar) {
				t.Errorf("error %q does not name %s", msg, tc.wantVar)
			}
			for _, v := range []string{fbSecret, fbKeyID} {
				if strings.Contains(msg, v) {
					t.Errorf("error leaks a value: %s", msg)
				}
			}
		})
	}
	t.Run("malformed line never echoes", func(t *testing.T) {
		_, _, err := LoadUploadFallback(writeR2(t, "garbage "+fbSecret+"\n", 0o600))
		if err == nil || strings.Contains(err.Error(), fbSecret) {
			t.Fatalf("err = %v", err)
		}
	})
}

package upload_test

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/upload"
)

func TestSigner_RoundTrip(t *testing.T) {
	t.Parallel()
	s := upload.NewSigner([]byte("synthetic-secret-key-for-tests-0001"))
	raw := s.URL("https://mm.example.com/", "u1", "take1", "audio.flac", 1000, 1700000000)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/worker/v1/uploads/u1" || u.Host != "mm.example.com" {
		t.Fatalf("url %s", raw)
	}
	exp, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
	sig := u.Query().Get("sig")
	if len(sig) != 43 || exp != 1700000000 {
		t.Fatalf("exp=%d sig=%q", exp, sig)
	}
	if !s.Verify("u1", "take1", "audio.flac", 1000, exp, sig) {
		t.Error("valid signature rejected")
	}
	for name, ok := range map[string]bool{
		"id":       s.Verify("u2", "take1", "audio.flac", 1000, exp, sig),
		"take":     s.Verify("u1", "take2", "audio.flac", 1000, exp, sig),
		"artifact": s.Verify("u1", "take1", "plan.json", 1000, exp, sig),
		"max":      s.Verify("u1", "take1", "audio.flac", 1001, exp, sig),
		"exp":      s.Verify("u1", "take1", "audio.flac", 1000, exp+1, sig),
		"empty":    s.Verify("u1", "take1", "audio.flac", 1000, exp, ""),
		"key":      upload.NewSigner([]byte("another-secret-key-for-tests-0002")).Verify("u1", "take1", "audio.flac", 1000, exp, sig),
	} {
		if ok {
			t.Errorf("tampered %s verified", name)
		}
	}
	st := s.StartURL("https://mm.example.com", "a1", "take1", 1700000000)
	su, _ := url.Parse(st)
	if su.Path != "/worker/v1/attempts/a1/start" || !s.VerifyStart("a1", "take1", 1700000000, su.Query().Get("sig")) || s.VerifyStart("a2", "take1", 1700000000, su.Query().Get("sig")) {
		t.Errorf("start url %s", st)
	}
	// an upload signature is never valid as a start signature (the artifact field differs)
	if s.VerifyStart("u1", "take1", exp, sig) {
		t.Error("upload signature accepted as start signature")
	}
}

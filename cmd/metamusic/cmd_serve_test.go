package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
)

// TestServe_MediaAndLibraryWired pins that serve() mounts the media handler and
// builds the library service and signer (M1-B5): an unsigned /media/ request
// is refused by the handler (403), not by the nil-Media 404 fallback, and the
// library-backed API answers for an authenticated caller.
func TestServe_MediaAndLibraryWired(t *testing.T) {
	env := testEnv(t, "local")
	code, tokOut, errs := do(t, env, "", "token", "create", "--name", "x", "--scopes", "read")
	if code != 0 {
		t.Fatalf("token create: %d %s", code, errs)
	}
	tok := strings.TrimSpace(tokOut)
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb syncBuf
	done := make(chan int, 1)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	})
	go func() {
		done <- run(ctx, []string{"serve"}, envFn(env), strings.NewReader(""), &out, &errb)
		close(done)
	}()
	client := &http.Client{Timeout: 10 * time.Second}
	var addr string
	for i := 0; i < 200 && addr == ""; i++ {
		time.Sleep(25 * time.Millisecond)
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, " ")
		}
	}
	if addr == "" {
		t.Fatalf("server did not start: %s %s", out.String(), errb.String())
	}
	resp, err := client.Get("http://" + addr + "/media/" + strings.Repeat("ab", 32) + ".flac")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(body), "MEDIA_LINK_INVALID") {
		t.Fatalf("media not wired: %d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/stats", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatalf("library not wired: %d %s", r2.StatusCode, b2)
	}
	// Signed link with an unknown sha: valid signature => 404 NOT_FOUND pins a
	// non-nil signer built from the secrets key.
	sec, err := config.LoadSecrets(env["MM_DATA_DIR"]+"/secrets", "local")
	if err != nil {
		t.Fatal(err)
	}
	link := mediaserve.NewSigner(sec.Key, time.Now).Sign(strings.Repeat("ab", 32), "flac", time.Hour, time.Minute).URL
	r3, err := client.Get("http://" + addr + link)
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if r3.StatusCode != 404 || !strings.Contains(string(b3), "NOT_FOUND") {
		t.Fatalf("signer not wired to media handler: %d %s", r3.StatusCode, b3)
	}
	// Options.Signer wired: the probe (not the signer) is the missing component.
	req4, _ := http.NewRequest("GET", "http://"+addr+"/api/device-probe", nil)
	req4.Header.Set("Authorization", "Bearer "+tok)
	r4, err := client.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	b4, _ := io.ReadAll(r4.Body)
	r4.Body.Close()
	if r4.StatusCode != 503 || !strings.Contains(string(b4), `"component":"probe"`) {
		t.Fatalf("Options.Signer not wired: %d %s", r4.StatusCode, b4)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

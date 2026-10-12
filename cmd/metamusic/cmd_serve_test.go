package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestServe_MediaAndLibraryWired pins that serve() mounts the media handler and
// builds the library service and signer (M1-B5): an unsigned /media/ request
// is refused by the handler (403), not by the nil-Media 404 fallback, and the
// library-backed API answers for an authenticated caller.
func TestServe_MediaAndLibraryWired(t *testing.T) {
	env := testEnv(t, "local")
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb syncBuf
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve"}, envFn(env), strings.NewReader(""), &out, &errb) }()
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
	resp, err := http.Get("http://" + addr + "/media/" + strings.Repeat("ab", 32) + ".flac")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(body), "MEDIA_LINK_INVALID") {
		t.Fatalf("media not wired: %d %s", resp.StatusCode, body)
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

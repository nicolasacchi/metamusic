package upload

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// A client that stops sending must not hold the per-id lock: the body has an idle deadline, the PUT answers
// 408 UPLOAD_STALLED, no part file stays, and an honest retry of the same id succeeds.
func TestUploadHandler_StalledBodyReleasesLock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.svc.idle = 150 * time.Millisecond
	srv := httptest.NewServer(r.svc.Handler())
	defer srv.Close()
	id, raw := r.target("audio.flac")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("abc"), 100)
	// a raw connection controls exactly which bytes reach the server (http.Client buffers a streamed body)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "PUT %s HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\nContent-Digest: %s\r\n\r\n", u.RequestURI(), len(body), digestOf(body))
	_, _ = conn.Write(body[:100]) // some bytes, then silence
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled PUT got no answer: %v", err)
	}
	var p struct{ Code string }
	_ = json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout || p.Code != "UPLOAD_STALLED" {
		t.Fatalf("stalled PUT: %d %q", resp.StatusCode, p.Code)
	}
	for i := 0; r.svc.inFlight(id); i++ { // the lock is released as the handler returns
		if i > 1000 {
			t.Fatal("lock still held after the stall")
		}
		time.Sleep(time.Millisecond)
	}
	r.noFiles(id)
	if st := r.str(`SELECT state FROM uploads WHERE id = ?`, id); st != "open" {
		t.Errorf("row state %q, want open", st)
	}
	if w := r.put(raw, body, nil); w.Code != 201 {
		t.Errorf("retry: %d %s", w.Code, w.Body.String())
	}
}

package upload

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
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

// The idle deadline measures client silence only: a handler blocked on a slow disk between two reads (the
// client's bytes sit in the socket buffer) must not turn into a timeout.
func TestUploadHandler_SlowDiskIsNotClientSilence(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.svc.idle = 100 * time.Millisecond
	var once sync.Once
	r.svc.beforeWrite = func() { once.Do(func() { time.Sleep(400 * time.Millisecond) }) } // 4x the idle window
	srv := httptest.NewServer(r.svc.Handler())
	defer srv.Close()
	id, raw := r.target("audio.flac")
	body := bytes.Repeat([]byte("abc"), 100)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "PUT %s HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\nContent-Digest: %s\r\n\r\n", raw, len(body), digestOf(body))
	_, _ = conn.Write(body[:100])
	time.Sleep(50 * time.Millisecond) // the handler reads these 100 bytes, then blocks 400 ms in its write
	_, _ = conn.Write(body[100:])     // the rest waits in the socket buffer until the disk is back
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("slow disk: %d", resp.StatusCode)
	}
	if st := r.str(`SELECT state FROM uploads WHERE id = ?`, id); st != "received" {
		t.Errorf("row state %q", st)
	}
}

// putAsync sends a PUT through the handler in a goroutine and returns its result channel.
func (r *rig) putAsync(target string, body io.Reader, length int, digest []byte) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPut, target, body)
		req.ContentLength = int64(length)
		req.Header.Set("Content-Digest", digestOf(digest))
		w := httptest.NewRecorder()
		r.svc.Handler().ServeHTTP(w, req)
		out <- w
	}()
	return out
}

func (r *rig) waitFlight(id string) {
	r.t.Helper()
	for i := 0; !r.svc.inFlight(id); i++ {
		if i > 3000 {
			r.t.Fatal("PUT never took the lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-ch:
		return w
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
		return nil
	}
}

// A second PUT of an id waits for the holder instead of answering 409 at once (an unmodified worker treats
// that 409 as fatal), then answers from the row as the holder left it.
func TestUploadHandler_SecondPutWaitsForHolder(t *testing.T) {
	t.Parallel()
	body := bytes.Repeat([]byte("abc"), 100)

	t.Run("holder succeeds after a disk stall", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		gate := make(chan struct{})
		var n atomic.Int32
		r.svc.beforeWrite = func() {
			if n.Add(1) == 1 {
				<-gate
			}
		}
		first := r.putAsync(u, bytes.NewReader(body), len(body), body)
		r.waitFlight(id)
		second := r.putAsync(u, bytes.NewReader(body), len(body), body)
		select {
		case w := <-second:
			t.Fatalf("second PUT answered while the holder was blocked: %d", w.Code)
		case <-time.After(150 * time.Millisecond):
		}
		close(gate)
		if w := recv(t, first); w.Code != 201 {
			t.Errorf("first: %d %s", w.Code, w.Body.String())
		}
		if w := recv(t, second); w.Code != 200 {
			t.Errorf("second: %d %s", w.Code, w.Body.String())
		}
		if c := r.count(`SELECT COUNT(*) FROM job_events WHERE event='upload'`); c != 1 {
			t.Errorf("upload events %d", c)
		}
	})

	t.Run("holder fails, the waiter delivers", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		pr, pw := io.Pipe()
		first := r.putAsync(u, pr, len(body), body)
		_, _ = pw.Write(body[:50])
		r.waitFlight(id)
		second := r.putAsync(u, bytes.NewReader(body), len(body), body)
		time.Sleep(100 * time.Millisecond)
		pw.Close() // the client is gone: short body
		if w := recv(t, first); w.Code != 400 || code(t, w) != "LENGTH_MISMATCH" {
			t.Errorf("first: %d %s", w.Code, w.Body.String())
		}
		if w := recv(t, second); w.Code != 201 {
			t.Errorf("second: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("lock never freed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.svc.lockWait = 80 * time.Millisecond
		id, u := r.target("audio.flac")
		pr, pw := io.Pipe()
		first := r.putAsync(u, pr, len(body), body)
		r.waitFlight(id)
		w := recv(t, r.putAsync(u, bytes.NewReader(body), len(body), body))
		if w.Code != 409 || code(t, w) != "UPLOAD_IN_PROGRESS" || w.Header().Get("Retry-After") != "2" {
			t.Errorf("second: %d %s", w.Code, w.Body.String())
		}
		_, _ = pw.Write(body)
		pw.Close()
		if w := recv(t, first); w.Code != 201 {
			t.Errorf("first: %d", w.Code)
		}
	})

	t.Run("waiter stops when its request is cancelled", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		pr, pw := io.Pipe()
		first := r.putAsync(u, pr, len(body), body)
		r.waitFlight(id)
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPut, u, bytes.NewReader(body)).WithContext(ctx)
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Digest", digestOf(body))
		done := make(chan struct{})
		go func() { r.svc.Handler().ServeHTTP(httptest.NewRecorder(), req); close(done) }()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled waiter still waiting")
		}
		_, _ = pw.Write(body)
		pw.Close()
		recv(t, first)
	})
}

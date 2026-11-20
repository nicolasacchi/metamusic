package objstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/objstore/objstoretest"
)

func newClient(t *testing.T, s *objstoretest.Server) *objstore.Client {
	t.Helper()
	c, err := objstore.New(s.Config(), s.HTTPClient(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func presignedPut(t *testing.T, c *objstore.Client, key string, body []byte) int {
	t.Helper()
	u, err := c.PresignPut(key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, u, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestObjstore_AgainstFake(t *testing.T) {
	ctx := context.Background()
	s := objstoretest.New(t)
	c := newClient(t, s)
	data := []byte("alice and bob sing")

	// a missing object
	if _, ok, err := c.Head(ctx, "mm/none"); err != nil || ok {
		t.Fatalf("head missing: ok=%v err=%v", ok, err)
	}
	// worker-style presigned PUT (a plain HTTP client, no credentials)
	if code := presignedPut(t, c, "mm/up-1", data); code != 200 {
		t.Fatalf("presigned put status %d", code)
	}
	size, ok, err := c.Head(ctx, "mm/up-1")
	if err != nil || !ok || size != int64(len(data)) {
		t.Fatalf("head: %d %v %v", size, ok, err)
	}
	rc, n, err := c.Get(ctx, "mm/up-1", 1<<20)
	if err != nil || n != int64(len(data)) {
		t.Fatalf("get: %d %v", n, err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("body %q", got)
	}
	// self-check Put and Delete (204 and 404 both succeed)
	if err := c.Put(ctx, "mm/selftest-1", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if !s.Has("mm/selftest-1") {
		t.Fatal("put did not store")
	}
	for i := 0; i < 2; i++ {
		if err := c.Delete(ctx, "mm/selftest-1"); err != nil {
			t.Fatalf("delete #%d: %v", i, err)
		}
	}
	if s.Has("mm/selftest-1") {
		t.Fatal("delete did not remove")
	}
	// keys with characters that need encoding sign correctly
	if err := c.Put(ctx, "mm/a b+c&d=e~f", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("encoded key: %v", err)
	}
	if _, ok, err := c.Head(ctx, "mm/a b+c&d=e~f"); err != nil || !ok {
		t.Fatalf("encoded key head: %v %v", ok, err)
	}
}

func TestObjstore_FakeVerifiesSignatures(t *testing.T) {
	ctx := context.Background()
	s := objstoretest.New(t)
	bad := s.Config()
	bad.SecretAccessKey = "wrong" + "secret" + "0123456789abcdef"
	c, err := objstore.New(bad, s.HTTPClient(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Head(ctx, "mm/x")
	var oe *objstore.Error
	if !errors.As(err, &oe) || oe.Status != 403 {
		t.Fatalf("wrong secret: %v", err)
	}
	// a tampered presigned URL is refused, as is an expired one
	good := newClient(t, s)
	u, _ := good.PresignPut("mm/x", time.Minute)
	req, _ := http.NewRequest(http.MethodPut, strings.Replace(u, "mm/x", "mm/y", 1), strings.NewReader("z"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("tampered status %d", resp.StatusCode)
	}
	past, _ := objstore.New(s.Config(), s.HTTPClient(), func() time.Time { return time.Now().Add(-time.Hour) })
	u, _ = past.PresignPut("mm/x", time.Minute)
	req, _ = http.NewRequest(http.MethodPut, u, strings.NewReader("z"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("expired status %d", resp.StatusCode)
	}
}

func TestObjstore_FaultInjection(t *testing.T) {
	ctx := context.Background()
	s := objstoretest.New(t)
	c := newClient(t, s)
	s.Store("mm/k", []byte("abc"))

	s.FailNext(1, 503)
	err := c.Delete(ctx, "mm/k")
	var oe *objstore.Error
	if !errors.As(err, &oe) || oe.Status != 503 || oe.Code != "SlowDown" {
		t.Fatalf("503: %v", err)
	}
	if _, ok, err := c.Head(ctx, "mm/k"); err != nil || !ok {
		t.Fatalf("after one failure: %v %v", ok, err)
	}
	s.ResetNext(3) // the HTTP client retries an idempotent request once on a reset
	if _, _, err := c.Head(ctx, "mm/k"); err == nil {
		t.Fatal("reset: want transport error")
	}
	s.SetDelay(300 * time.Millisecond)
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, err := c.Head(cctx, "mm/k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow: want deadline, got %v", err)
	}
}

func TestObjstore_GetBoundedTooLarge(t *testing.T) {
	ctx := context.Background()
	s := objstoretest.New(t)
	c := newClient(t, s)
	s.Store("mm/big", bytes.Repeat([]byte{7}, 1000))

	// declared length over the bound: refused before the body is read
	if _, _, err := c.Get(ctx, "mm/big", 999); !errors.Is(err, objstore.ErrTooLarge) {
		t.Fatalf("declared: %v", err)
	}
	if rc, n, err := c.Get(ctx, "mm/big", 1000); err != nil || n != 1000 {
		t.Fatalf("exact: %d %v", n, err)
	} else {
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || len(b) != 1000 {
			t.Fatalf("read exact: %d %v", len(b), err)
		}
	}
	// a lying server (chunked, no length): the reader stops with ErrTooLarge
	s.HideLength(true)
	rc, _, err := c.Get(ctx, "mm/big", 500)
	if err != nil {
		if !errors.Is(err, objstore.ErrTooLarge) {
			t.Fatalf("hidden: %v", err)
		}
		return
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); !errors.Is(err, objstore.ErrTooLarge) {
		t.Fatalf("hidden read: %v", err)
	}
	if _, _, err := c.Get(ctx, "mm/nope", 10); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestObjstore_ErrorsRedacted(t *testing.T) {
	ctx := context.Background()
	s := objstoretest.New(t)
	cfg := s.Config()
	c := newClient(t, s)
	const key = "mm/0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: want error", name)
		}
		msg := err.Error()
		for _, bad := range []string{cfg.SecretAccessKey, cfg.AccessKeyID, key, "X-Amz-Signature", "Signature=", "Credential=", "StringToSign"} {
			if strings.Contains(msg, bad) {
				t.Errorf("%s: error contains %q: %s", name, bad, msg)
			}
		}
	}
	// S3 echoes credentials and the string to sign in its XML error body; none of it may survive
	s.FailNextBody(1, 403, `<?xml version="1.0"?><Error><Code>SignatureDoesNotMatch</Code><Message>bad `+key+`</Message><AWSAccessKeyId>`+cfg.AccessKeyID+`</AWSAccessKeyId><StringToSign>AWS4-HMAC-SHA256 `+cfg.SecretAccessKey+`</StringToSign></Error>`)
	_, _, err := c.Get(ctx, key, 10)
	check("403 xml", err)
	var oe *objstore.Error
	if !errors.As(err, &oe) || oe.Status != 403 || oe.Code != "SignatureDoesNotMatch" {
		t.Fatalf("parsed error = %#v", err)
	}
	s.FailNextBody(1, 500, "<html>"+cfg.SecretAccessKey+key+"</html>")
	check("500 garbage", c.Delete(ctx, key))
	s.ResetNext(3) // the HTTP client retries an idempotent request once on a reset
	_, _, err = c.Head(ctx, key)
	check("reset", err)
	s.Close()
	_, _, err = c.Head(ctx, key)
	check("closed", err)
	check("put closed", c.Put(ctx, key, strings.NewReader("x"), 1))
	_, _, err = c.Get(ctx, key, 10)
	check("get closed", err)
}

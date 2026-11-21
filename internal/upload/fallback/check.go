package fallback

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// selftestBytes is the size of the self-check object.
const selftestBytes = 1024

// MaxSkew is the largest difference between this host's clock and the bucket's that the self-check accepts: a presigned URL is only
// as good as the clocks agree.
const MaxSkew = 5 * time.Minute

// CheckStep is one line of the self-check report.
type CheckStep struct {
	Name string
	OK   bool
	Note string // short and free of any URL, key or credential
}

// SelfCheck is `metamusic upload fallback check` (C399.8), spend-free: a presigned PUT of 1 KiB of random bytes under prefix +
// "selftest-" + uuid (the way a worker puts), HEAD, GET with a sha256 comparison, DELETE, HEAD answering 404, and the clock skew read
// from the PUT answer's Date header. It stops at the first failed step; the object is deleted on a best-effort basis even then.
func SelfCheck(ctx context.Context, objs Objects, hc *http.Client, prefix string, now func() time.Time) ([]CheckStep, error) {
	var steps []CheckStep
	fail := func(name string, err error) ([]CheckStep, error) {
		steps = append(steps, CheckStep{Name: name, Note: redact.Error(err)})
		return steps, fmt.Errorf("fallback check: %s failed", name)
	}
	ok := func(name, note string) { steps = append(steps, CheckStep{Name: name, OK: true, Note: note}) }

	body := make([]byte, selftestBytes)
	if _, err := rand.Read(body); err != nil {
		return fail("random", err)
	}
	want := sha256.Sum256(body)
	key := prefix + "selftest-" + uuid.NewV7().String()
	url, err := objs.PresignPut(key, 5*time.Minute)
	if err != nil {
		return fail("presign", err)
	}
	cleanup := func() { _ = objs.Delete(context.WithoutCancel(ctx), key) }
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fail("put", errors.New("could not build the request"))
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "metamusic-fallback-check/1") // Cloudflare answers 1010 to a bare library agent
	resp, err := hc.Do(req)
	if err != nil {
		return fail("put", errors.New("the request failed")) // the transport error names the URL: never print it
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		cleanup()
		return fail("put", fmt.Errorf("the bucket answered %d", resp.StatusCode))
	}
	ok("put", "presigned PUT accepted")
	defer func() {
		if len(steps) > 0 && !steps[len(steps)-1].OK {
			cleanup()
		}
	}()
	switch size, found, err := objs.Head(ctx, key); {
	case err != nil:
		return fail("head", err)
	case !found || size != selftestBytes:
		return fail("head", errors.New("the object is missing or has another size"))
	}
	ok("head", "size matches")
	rc, _, err := objs.Get(ctx, key, selftestBytes)
	if err != nil {
		return fail("get", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fail("get", err)
	}
	if sha256.Sum256(got) != want {
		return fail("get", errors.New("the sha256 differs"))
	}
	ok("get", "sha256 matches")
	if err := objs.Delete(ctx, key); err != nil {
		return fail("delete", err)
	}
	ok("delete", "deleted")
	if _, found, err := objs.Head(ctx, key); err != nil {
		return fail("head-after-delete", err)
	} else if found {
		return fail("head-after-delete", errors.New("the object is still there"))
	}
	ok("head-after-delete", "404 as expected")
	if d := resp.Header.Get("Date"); d != "" {
		t, perr := http.ParseTime(d)
		if perr != nil {
			return fail("clock", errors.New("the bucket's Date header is not a time"))
		}
		skew := now().Sub(t)
		if skew < -MaxSkew || skew > MaxSkew {
			return fail("clock", fmt.Errorf("the clock differs from the bucket's by %s (limit %s)", skew.Round(time.Second), MaxSkew))
		}
		ok("clock", fmt.Sprintf("skew %s", skew.Round(time.Second)))
	} else {
		return fail("clock", errors.New("the bucket sent no Date header"))
	}
	return steps, nil
}

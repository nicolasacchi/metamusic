package objstore_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/objstore/objstoretest"
)

// TestObjstoreTest_TLSAndVirtualClock: NewTLS serves https (what r2.env requires), and UseClock makes the fake judge expiry and skew
// by a rig's clock: a client on that clock works, a client on the wall clock is refused as skewed.
func TestObjstoreTest_TLSAndVirtualClock(t *testing.T) {
	t.Parallel()
	s := objstoretest.NewTLS(t)
	if !strings.HasPrefix(s.Config().Endpoint, "https://") {
		t.Fatalf("endpoint %q", s.Config().Endpoint)
	}
	virtual := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s.UseClock(func() time.Time { return virtual })
	on, err := objstore.New(s.Config(), s.HTTPClient(), func() time.Time { return virtual })
	if err != nil {
		t.Fatal(err)
	}
	if err := on.Put(t.Context(), "mm/k", strings.NewReader("abc"), 3); err != nil {
		t.Fatalf("put on the shared clock: %v", err)
	}
	if n, ok, err := on.Head(t.Context(), "mm/k"); err != nil || !ok || n != 3 {
		t.Fatalf("head: %d %v %v", n, ok, err)
	}
	virtual = virtual.Add(2 * time.Hour) // the server moves on; a client stuck in the past is skewed
	late, err := objstore.New(s.Config(), s.HTTPClient(), func() time.Time { return virtual.Add(-2 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	if err := late.Put(t.Context(), "mm/k3", strings.NewReader("abc"), 3); err == nil {
		t.Error("a request two hours behind the server clock was accepted")
	}
}

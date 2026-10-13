package idem_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newIdem(t *testing.T) (*idem.Store, *clock, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &clock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	return idem.NewStore(st, c.now), c, st
}

func TestRequestSHA(t *testing.T) {
	q1 := url.Values{"a": {"1"}, "b": {"2"}}
	q2 := url.Values{"b": {"2"}, "a": {"1"}}
	base, _ := idem.RequestSHA("op", "POST", "/api/x", q1, []byte(`{"a":1,"b":[1,2]}`))
	same, _ := idem.RequestSHA("op", "POST", "/api/x", q2, []byte(` {"b":[1,2], "a":1} `))
	if base != same {
		t.Fatal("query order and JSON key order must not matter")
	}
	for name, f := range map[string]func() (string, error){
		"op": func() (string, error) {
			return idem.RequestSHA("op2", "POST", "/api/x", q1, []byte(`{"a":1,"b":[1,2]}`))
		},
		"path": func() (string, error) {
			return idem.RequestSHA("op", "POST", "/api/y", q1, []byte(`{"a":1,"b":[1,2]}`))
		},
		"body": func() (string, error) {
			return idem.RequestSHA("op", "POST", "/api/x", q1, []byte(`{"a":2,"b":[1,2]}`))
		},
	} {
		if got, _ := f(); got == base {
			t.Errorf("%s must change the hash", name)
		}
	}
	raw, err := idem.RequestSHA("op", "POST", "/api/x", nil, []byte("not json"))
	if err != nil || raw == "" {
		t.Fatalf("non-JSON body must hash raw: %v", err)
	}
}

func TestIdempotency_Prune(t *testing.T) {
	s, c, st := newIdem(t)
	ctx := context.Background()
	if _, err := s.Begin(ctx, "agent:a", "k1", "op", "sha", true); err != nil { // derived: 10 min
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, "agent:a", "k2", "op", "sha", false); err != nil { // 24 h
		t.Fatal(err)
	}
	c.t = c.t.Add(10*time.Minute + 59*time.Minute)
	if n, _ := s.Prune(ctx); n != 0 {
		t.Fatalf("pruned %d before the 1 h grace", n)
	}
	c.t = c.t.Add(2 * time.Minute)
	if n, err := s.Prune(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left int
	_ = st.R.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&left)
	if left != 1 {
		t.Fatalf("left %d", left)
	}
}

func TestDerivedKey(t *testing.T) {
	a, _ := idem.DerivedKey("agent:1", "op", []byte(`{"x":1,"y":2}`))
	b, _ := idem.DerivedKey("agent:1", "op", []byte(`{"y":2,"x":1}`))
	c, _ := idem.DerivedKey("agent:2", "op", []byte(`{"x":1,"y":2}`))
	if a != b || a == c || !strings.HasPrefix(a, "mcp:") {
		t.Fatalf("%s %s %s", a, b, c)
	}
	if _, err := idem.DerivedKey("p", "op", []byte(`{"x":`)); err == nil {
		t.Fatal("invalid args must error")
	}
}

func TestRedact(t *testing.T) {
	got := string(idem.Redact([]byte(`{"id":"1","token":"s3cret","n":2}`), []string{"token"}))
	if strings.Contains(got, "s3cret") || !strings.Contains(got, `"token":null`) || !strings.Contains(got, `"id":"1"`) {
		t.Fatal(got)
	}
	if string(idem.Redact([]byte(`[1]`), []string{"token"})) != `[1]` {
		t.Fatal("non-object body must be unchanged")
	}
	if string(idem.Redact([]byte(`{"a":1}`), nil)) != `{"a":1}` {
		t.Fatal("no fields: unchanged")
	}
}

func TestStore_Lifecycle(t *testing.T) {
	s, c, st := newIdem(t)
	ctx := context.Background()
	begin := func(sha string) idem.Begun {
		t.Helper()
		b, err := s.Begin(ctx, "agent:a", "k", "op", sha, false)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := begin("s1"); b.Outcome != idem.Proceed {
		t.Fatalf("first: %v", b.Outcome)
	}
	if b := begin("s1"); b.Outcome != idem.InProgress {
		t.Fatalf("pending same: %v", b.Outcome)
	}
	if b := begin("s2"); b.Outcome != idem.Conflict {
		t.Fatalf("pending other: %v", b.Outcome)
	}
	lease := func() string {
		var l string
		_ = st.R.QueryRow(`SELECT lease_until FROM idempotency`).Scan(&l)
		return l
	}
	before := lease()
	c.t = c.t.Add(time.Minute)
	if err := s.Extend(ctx, "agent:a", "k"); err != nil {
		t.Fatal(err)
	}
	if lease() <= before {
		t.Fatal("Extend did not move the lease")
	}
	if err := s.Complete(ctx, "agent:a", "k", 201, "application/json", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	b := begin("s1")
	if b.Outcome != idem.Replay || b.Status != 201 || b.ContentType != "application/json" || string(b.Body) != `{"a":1}` {
		t.Fatalf("replay: %+v", b)
	}
	if b := begin("s2"); b.Outcome != idem.Conflict {
		t.Fatalf("done other: %v", b.Outcome)
	}
	// Release only drops pending rows.
	if err := s.Release(ctx, "agent:a", "k"); err != nil {
		t.Fatal(err)
	}
	if b := begin("s1"); b.Outcome != idem.Replay {
		t.Fatal("Release removed a done row")
	}
	if _, err := s.Begin(ctx, "agent:a", "k2", "op", "s", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, "agent:a", "k2"); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Begin(ctx, "agent:a", "k2", "op", "other", false); b.Outcome != idem.Proceed {
		t.Fatal("released key must be reusable with another body")
	}
	// Expired done rows are reclaimed by the upsert.
	c.t = c.t.Add(25 * time.Hour)
	if b := begin("s2"); b.Outcome != idem.Proceed {
		t.Fatalf("expired: %v", b.Outcome)
	}
}

func TestDerivedContext(t *testing.T) {
	if _, ok := idem.DerivedFrom(context.Background()); ok {
		t.Fatal("unexpected marker")
	}
	ctx := idem.WithDerived(context.Background(), "mcp:x")
	if k, ok := idem.DerivedFrom(ctx); !ok || k != "mcp:x" {
		t.Fatal(k)
	}
	if k, err := idem.DerivedKey("p", "op", nil); err != nil || k == "" {
		t.Fatalf("empty args: %v", err)
	}
	if got := idem.Redact([]byte(`{"a":1}`), []string{"token"}); string(got) != `{"a":1}` {
		t.Fatal("absent field must leave the body unchanged")
	}
	if got := idem.Redact([]byte(`null`), []string{"token"}); string(got) != `null` {
		t.Fatal(string(got))
	}
}

func TestNewStore_DefaultClock(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := idem.NewStore(st, nil)
	if b, err := s.Begin(context.Background(), "p", "k", "op", "s", false); err != nil || b.Outcome != idem.Proceed {
		t.Fatalf("%v %v", b, err)
	}
}

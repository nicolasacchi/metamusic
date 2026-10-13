package audit_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func newStore(t *testing.T) *audit.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := 0
	return audit.NewStore(st, func() string { n++; return fmt.Sprintf("id-%04d", n) })
}

func TestAudit_InsertAndList(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	err := s.Insert(ctx, audit.Entry{At: at, Principal: auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "bot"},
		Transport: reqinfo.MCP, Client: "c/1", Op: "t_create", Method: "POST", ArgsSHA256: "abc", Status: 201,
		Idempotency: audit.IdemStored, CreatedIDs: []string{"x1"}, Duration: 12 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.List(ctx, audit.Filter{})
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("list: %v %+v", err, p)
	}
	r := p.Items[0]
	if r.PrincipalID != "agent:t1" || r.Transport != "mcp" || r.Idempotency != "stored" || r.CreatedIDs[0] != "x1" || r.DurationMS != 12 || r.Status != 201 {
		t.Fatalf("row = %+v", r)
	}
}

func TestAudit_ListKeyset(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		op := "o"
		if i%2 == 0 {
			op = "e"
		}
		if err := s.Insert(ctx, audit.Entry{At: time.Now(), Principal: auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"},
			Transport: reqinfo.REST, Op: op, Method: "PATCH", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	cur := ""
	for {
		p, err := s.List(ctx, audit.Filter{Limit: 2, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range p.Items {
			got = append(got, r.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cur = p.NextCursor
	}
	want := "id-0005 id-0004 id-0003 id-0002 id-0001"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v want %s", got, want)
	}
	p, _ := s.List(ctx, audit.Filter{Op: "e"})
	if len(p.Items) != 3 {
		t.Fatalf("op filter: %d", len(p.Items))
	}
}

func TestAudit_Collector(t *testing.T) {
	ctx := audit.WithCollector(context.Background())
	audit.NoteCreated(ctx, "a", "b")
	audit.NoteCreated(ctx, "c")
	if got := strings.Join(audit.Created(ctx), ","); got != "a,b,c" {
		t.Fatal(got)
	}
	audit.NoteCreated(context.Background(), "x") // no collector: no panic
	if audit.Created(context.Background()) != nil {
		t.Fatal("expected nil without collector")
	}
}

func TestAudit_ListMultiValueFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	add := func(k auth.Kind, op string, status int) {
		t.Helper()
		if err := s.Insert(ctx, audit.Entry{At: at, Principal: auth.Principal{Kind: k, ID: "x", Name: "n"}, Transport: reqinfo.REST,
			Op: op, Method: "POST", Status: status, Duration: time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	add(auth.KindAgent, "a", 200)  // id-0001
	add(auth.KindHuman, "b", 404)  // id-0002
	add(auth.KindDevice, "c", 500) // id-0003
	add(auth.KindAgent, "c", 200)  // id-0004
	ids := func(f audit.Filter) string {
		t.Helper()
		p, err := s.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range p.Items {
			out = append(out, r.ID[3:])
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		f    audit.Filter
		want string
	}{
		{audit.Filter{PrincipalKinds: []string{"agent", "device"}}, "0004,0003,0001"},
		{audit.Filter{Ops: []string{"a", "b"}}, "0002,0001"},
		{audit.Filter{ExcludeOps: []string{"c"}}, "0002,0001"},
		{audit.Filter{StatusMin: 400}, "0003,0002"},
		{audit.Filter{PrincipalKinds: []string{"agent"}, ExcludeOps: []string{"a"}, StatusMin: 100}, "0004"},
	} {
		if got := ids(tc.f); got != tc.want {
			t.Errorf("%+v: got %s want %s", tc.f, got, tc.want)
		}
	}
}

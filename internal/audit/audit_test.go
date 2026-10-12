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

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

type recorder struct {
	got chan Message
	err error
}

func (r recorder) Notify(_ context.Context, m Message) error { r.got <- m; return r.err }

func TestNop(t *testing.T) {
	if err := (Nop{}).Notify(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestAsync_DeliversWithoutBlocking(t *testing.T) {
	rec := recorder{got: make(chan Message, 1)}
	n := Async(rec, time.Second)
	if err := n.Notify(context.Background(), Message{Title: "backup failed", Priority: 4}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-rec.got:
		if m.Title != "backup failed" || m.Priority != 4 {
			t.Fatalf("message = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered")
	}
}

func TestAsync_ErrorsAreSwallowed(t *testing.T) {
	rec := recorder{got: make(chan Message, 1), err: errors.New("dial tcp: refused")}
	if err := Async(rec, time.Second).Notify(context.Background(), Message{}); err != nil {
		t.Fatalf("Async must not return delivery errors: %v", err)
	}
	<-rec.got
}

func TestAsync_NilNotifier(t *testing.T) {
	if err := Async(nil, time.Second).Notify(context.Background(), Message{}); err != nil {
		t.Fatal(err)
	}
}

type ntfyCall struct {
	method, path, rawQuery, contentType, authorization string
	header                                             http.Header
	body                                               map[string]any
}

func ntfyServer(t *testing.T, status int) (*httptest.Server, func() []ntfyCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []ntfyCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, ntfyCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.Header.Clone(), body})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []ntfyCall { mu.Lock(); defer mu.Unlock(); return append([]ntfyCall(nil), calls...) }
}

const ntfyCanary = "canary-ntfy-bearer"

func TestNotify_Ntfy(t *testing.T) {
	srv, calls := ntfyServer(t, 200)
	n := NewNtfy(srv.URL+"/mm-topic", ntfyCanary, srv.Client())
	err := n.Notify(context.Background(), Message{Title: "Token created", Body: "Token created: bot (read)", Priority: 3, Tags: []string{"key"}, Click: "https://mm.example.com/human/tokens"})
	if err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
	c := got[0]
	if c.method != "POST" || c.path != "/" || c.rawQuery != "" || c.contentType != "application/json" || c.authorization != "Bearer "+ntfyCanary {
		t.Fatalf("request = %+v", c)
	}
	want := map[string]any{"topic": "mm-topic", "title": "Token created", "message": "Token created: bot (read)", "priority": float64(3),
		"tags": []any{"key"}, "click": "https://mm.example.com/human/tokens"}
	if diff := cmp.Diff(want, c.body); diff != "" {
		t.Fatalf("body mismatch (-want +got):\n%s", diff)
	}
	// only Content-Type, Authorization and Go's own transport headers travel
	for k := range c.header {
		switch k {
		case "Content-Type", "Authorization", "User-Agent", "Content-Length", "Accept-Encoding":
		default:
			t.Errorf("unexpected header %s", k)
		}
	}
}

func TestNotify_BasePathAndNoToken(t *testing.T) {
	srv, calls := ntfyServer(t, 200)
	n := NewNtfy(srv.URL+"/ntfy/sub/mm", "", nil)
	if err := n.Notify(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	c := calls()[0]
	if c.path != "/ntfy/sub" || c.authorization != "" || c.body["topic"] != "mm" {
		t.Fatalf("%+v", c)
	}
	if _, ok := c.body["priority"]; ok {
		t.Fatal("zero priority must be omitted")
	}
	if _, ok := c.body["tags"]; ok {
		t.Fatal("empty tags must be omitted")
	}
	// URL without a topic is rejected
	if err := NewNtfy(srv.URL, "", nil).Notify(context.Background(), Message{Title: "t"}); err == nil {
		t.Fatal("topic-less URL accepted")
	}
	if err := NewNtfy("://bad", "", nil).Notify(context.Background(), Message{Title: "t"}); err == nil {
		t.Fatal("bad URL accepted")
	}
}

func TestNotify_ControlCharsInNames(t *testing.T) {
	srv, calls := ntfyServer(t, 200)
	n := NewNtfy(srv.URL+"/mm", ntfyCanary, srv.Client())
	name := "a\r\nX-Evil: 1\u0085b\x00c"
	if err := n.Notify(context.Background(), Message{Title: "Device paired: " + name, Body: "Device paired: " + name, Priority: 3}); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	c := calls()[0]
	if c.header.Get("X-Evil") != "" {
		t.Fatal("injected header")
	}
	msg, _ := c.body["message"].(string)
	if msg != "Device paired: a  X-Evil: 1 b c" {
		t.Fatalf("message = %q", msg)
	}
	// caps: 200 runes title, 1000 runes message
	long := strings.Repeat("é", 2000)
	if err := n.Notify(context.Background(), Message{Title: long, Body: long}); err != nil {
		t.Fatal(err)
	}
	c = calls()[1]
	if utf8.RuneCountInString(c.body["title"].(string)) != 200 || utf8.RuneCountInString(c.body["message"].(string)) != 1000 {
		t.Fatal("caps not applied")
	}
}

func TestNotify_ErrorsRedacted(t *testing.T) {
	srv, _ := ntfyServer(t, 500)
	n := NewNtfy(srv.URL+"/mm?secret=1", ntfyCanary, srv.Client())
	err := n.Notify(context.Background(), Message{Title: "t"})
	if err == nil {
		t.Fatal("500 accepted")
	}
	if strings.Contains(err.Error(), ntfyCanary) || strings.Contains(err.Error(), "secret=1") {
		t.Fatalf("error leaks: %v", err)
	}
	// transport errors carry the URL: they must survive redact
	srv.Close()
	err = n.Notify(context.Background(), Message{Title: "t"})
	if err == nil {
		t.Fatal("closed server accepted")
	}
	if got := redact.Error(err); strings.Contains(got, ntfyCanary) || strings.Contains(got, "secret=1") {
		t.Fatalf("redacted error leaks: %v", got)
	}
}

func TestNotify_NoSecretsInMessages(t *testing.T) {
	// the events M2a emits, built the way the call sites build them
	msgs := []Message{
		DevicePaired("Phone"),
		TokenCreated("bot", []string{"read", "write"}),
		BackupFailed("BACKUP_FAILED"),
	}
	for _, m := range msgs {
		for _, s := range append([]string{m.Title, m.Body, m.Click}, m.Tags...) {
			for _, bad := range []string{"mmp_", "mmd_", "mm_dev", "mm_session", "http://", "https://", "?", "#"} {
				if strings.Contains(s, bad) {
					t.Errorf("%q contains %q", s, bad)
				}
			}
		}
		if m.Priority < 1 || m.Priority > 5 {
			t.Errorf("priority %d", m.Priority)
		}
	}
	if got := TokenCreated("bot", []string{"read", "write"}).Body; got != "Token created: bot (read, write)" {
		t.Fatalf("token body = %q", got)
	}
	if got := DevicePaired("Phone").Body; got != "Device paired: Phone" {
		t.Fatalf("device body = %q", got)
	}
	if m := BackupFailed("BACKUP_FAILED"); m.Priority != 4 || !strings.Contains(m.Body, "BACKUP_FAILED") {
		t.Fatalf("%+v", m)
	}
}

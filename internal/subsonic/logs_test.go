package subsonic

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestSubsonic_LogsNeverContainCredentials: the captured slog of 50 mixed calls holds no t, s, apiKey, p or u value,
// and exactly one line per request (endpoint, protocol status and code, HTTP status, principal kind, duration).
func TestSubsonic_LogsNeverContainCredentials(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	secrets := map[string]bool{}
	note := func(vs ...string) {
		for _, v := range vs {
			secrets[v] = true
		}
	}
	for i := 0; i < 50; i++ {
		ip := fmt.Sprintf("198.51.100.%d", 130+i%20)
		salt := fmt.Sprintf("logsalt-%02d-xyz", i)
		var q url.Values
		switch i % 5 {
		case 0:
			q = tokenParams("rwclient", r.rwPW, salt)
			note(q.Get("t"), salt)
		case 1:
			q = keyParams(r.rwKey)
			note(r.rwKey)
		case 2:
			q = tokenParams("rwclient", "wrong-"+salt, salt)
			note(q.Get("t"), salt)
		case 3:
			bad := fmt.Sprintf("mms_%052d", i)
			q = keyParams(bad)
			note(bad)
		case 4:
			q = url.Values{"u": {"someone-" + salt}, "p": {"hunter2-" + salt}, "c": {"app"}, "v": {"1.16.1"}, "f": {"json"}}
			note("hunter2-"+salt, "someone-"+salt)
		}
		r.get("ping", q, ip)
	}
	note(r.rwPW, r.roPW, r.rwKey, r.roKey)
	logs := r.logs.String()
	for s := range secrets {
		if strings.Contains(logs, s) {
			t.Fatalf("logs contain a credential value %q", s)
		}
	}
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	if len(lines) != 50 {
		t.Fatalf("log lines = %d, want 50", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"endpoint", "status", "code", "http", "principal", "duration"} {
		if _, ok := first[k]; !ok {
			t.Errorf("log line lacks %q: %s", k, lines[0])
		}
	}
	if strings.Contains(logs, "apiKey") || strings.Contains(logs, "rwclient") || strings.Contains(logs, "testapp") {
		t.Error("logs name a parameter value or the username")
	}
}

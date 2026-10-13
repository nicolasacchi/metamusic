package notify

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestNotify_ActionsJSON: Message.Actions is published as ntfy's JSON "actions" array (M3 C82).
func TestNotify_ActionsJSON(t *testing.T) {
	t.Parallel()
	srv, calls := ntfyServer(t, 200)
	n := NewNtfy(srv.URL+"/mm-topic", "", srv.Client())
	err := n.Notify(context.Background(), Message{Title: "Approval needed", Body: "Agent bot asks to spend up to $0.70 for 7 takes", Priority: 4,
		Click: "https://mm.example.com/human/confirm/b1",
		Actions: []Action{
			{Action: "view", Label: "Review", URL: "https://mm.example.com/human/confirm/b1"},
			{Label: "Open console", URL: "https://console.example.com/serverless"}, // empty Action means view
			{Action: "view", Label: "Jobs", URL: "https://mm.example.com/jobs"},
			{Action: "view", Label: "Fourth is dropped", URL: "https://mm.example.com/x"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
	want := []any{
		map[string]any{"action": "view", "label": "Review", "url": "https://mm.example.com/human/confirm/b1"},
		map[string]any{"action": "view", "label": "Open console", "url": "https://console.example.com/serverless"},
		map[string]any{"action": "view", "label": "Jobs", "url": "https://mm.example.com/jobs"},
	}
	if diff := cmp.Diff(want, got[0].body["actions"]); diff != "" {
		t.Fatalf("actions mismatch (-want +got):\n%s", diff)
	}
}

func TestNotify_ActionsSanitised(t *testing.T) {
	t.Parallel()
	srv, calls := ntfyServer(t, 200)
	n := NewNtfy(srv.URL+"/mm-topic", "", srv.Client())
	err := n.Notify(context.Background(), Message{Title: "t", Body: "b", Actions: []Action{
		{Action: "http", Label: "POST somewhere", URL: "https://mm.example.com/x"},    // only view is supported
		{Action: "view", Label: "", URL: "https://mm.example.com/x"},                  // label required
		{Action: "view", Label: "no url"},                                             // url required
		{Action: "view", Label: "bad\r\nurl", URL: "https://mm.example.com/\r\nX: y"}, // control characters in the URL
		{Action: "view", Label: "  Good\nlabel  ", URL: "https://mm.example.com/ok"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	acts, _ := calls()[0].body["actions"].([]any)
	if len(acts) != 1 {
		t.Fatalf("actions = %v", acts)
	}
	a := acts[0].(map[string]any)
	if a["label"] != "Good label" || a["url"] != "https://mm.example.com/ok" || a["action"] != "view" {
		t.Fatalf("action = %v", a)
	}
	// no actions ⇒ the key is omitted
	if err := n.Notify(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := calls()[1].body["actions"]; ok {
		t.Fatal("empty actions must be omitted")
	}
}

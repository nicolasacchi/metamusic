package jobs

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/notify"
)

func TestNotify_M3Events(t *testing.T) {
	t.Parallel()
	const pub = "https://mm.example.com/"
	const console = "https://console.example.com/serverless"
	for _, c := range []struct {
		name     string
		m        notify.Message
		priority int
		title    string
		body     string
		click    string
		actions  []notify.Action
		tags     []string
	}{
		{"approval", NoteApprovalRequested(pub, "claude-agent", 7, 691731, "bat-1"), 4, "Approval needed", "Agent claude-agent asks to spend up to $0.70 for 7 takes",
			"https://mm.example.com/human/confirm/bat-1", []notify.Action{{Action: "view", Label: "Review", URL: "https://mm.example.com/human/confirm/bat-1"}}, nil},
		{"batch", NoteBatchDone(pub, "bat-1", "6 completed (1 truncated), 1 failed - $0.15 provisional"), 3, "Batch done", "6 completed (1 truncated), 1 failed - $0.15 provisional",
			"https://mm.example.com/batches/bat-1", nil, nil},
		{"tripped", NoteProviderTripped(pub, "stuck_running", "purged the queue"), 5, "Generation stopped: stuck_running", "purged the queue", "https://mm.example.com/jobs", nil, nil},
		{"scale", NoteManualScaleToZero(console, nil, 0), 5, "Set max workers to 0 now", "The app cannot change the endpoint; open the console", console,
			[]notify.Action{{Action: "view", Label: "Open console", URL: console}}, []string{"rotating_light"}},
		{"access", NoteProviderAccessLost(pub), 5, "RunPod keys refused", "refused", "https://mm.example.com/jobs", nil, []string{"rotating_light"}},
		{"meter", NoteMeterExceeds(pub), 5, "Worker time above reservation", "", "https://mm.example.com/jobs", nil, nil},
		{"workers", NoteWorkersNotZero(pub), 4, "Workers still running", "", "https://mm.example.com/jobs", nil, nil},
		{"cancel", NoteCancelNotConfirmed(pub, "job-1"), 4, "Cancel not confirmed yet", "", "https://mm.example.com/jobs/job-1", nil, nil},
		{"drift", NoteConfigDrift(pub, []string{"gpu.selection", "workers.max"}, true), 4, "Endpoint drift: gpu.selection, workers.max", "Reverted", "https://mm.example.com/jobs", nil, nil},
		{"drift not reverted", NoteConfigDrift(pub, []string{"workers.max"}, false), 4, "Endpoint drift: workers.max", "Not reverted", "https://mm.example.com/jobs", nil, nil},
		{"skew", NoteClockSkew(pub, 45), 4, "Clock skew 45 s - billing releases paused", "", "https://mm.example.com/jobs", nil, nil},
		{"stalled", NoteBillingStalled(pub, []string{"b1", "b2"}), 4, "Billing reconciliation stalled", "b1, b2", "https://mm.example.com/jobs", nil, nil},
		{"preflight", NotePreflightFailing(pub, "disk low"), 4, "Generation paused: disk low", "", "https://mm.example.com/jobs", nil, nil},
		{"unreadable", NoteBillingUnreadable(pub, 3), 2, "Billing not reconciled", "3 batches released after the hold", "https://mm.example.com/jobs", nil, nil},
		{"idle", NoteIdleDrift(pub, 90, 20), 4, "Endpoint drift: idle timeout", "90 s", "https://mm.example.com/jobs", nil, nil},
	} {
		m := c.m
		if m.Priority != c.priority || m.Title != c.title || !strings.Contains(m.Body, c.body) || m.Click != c.click {
			t.Errorf("%s: %+v", c.name, m)
		}
		if len(m.Actions) != len(c.actions) {
			t.Errorf("%s: actions %v", c.name, m.Actions)
		}
		for i := range c.actions {
			if m.Actions[i] != c.actions[i] {
				t.Errorf("%s: action %d = %+v", c.name, i, m.Actions[i])
			}
		}
		if strings.Join(m.Tags, ",") != strings.Join(c.tags, ",") {
			t.Errorf("%s: tags %v", c.name, m.Tags)
		}
		if len(m.Actions) > notify.MaxActions {
			t.Errorf("%s: too many actions", c.name)
		}
	}
}

// TestNotify_M3NoSecrets: no message carries a token, a query string, an endpoint id, a key or a ref.
func TestNotify_M3NoSecrets(t *testing.T) {
	t.Parallel()
	pub := "https://mm.example.com"
	var all []notify.Message
	all = append(all, NoteApprovalRequested(pub, "agent", 1, 100, "b"), NoteBatchDone(pub, "b", "1 completed"), NoteProviderTripped(pub, "crash_loop", "cancelled jobs: 1"),
		NoteManualScaleToZero("https://console.example.com/serverless", nil, 0), NoteProviderAccessLost(pub), NoteMeterExceeds(pub), NoteWorkersNotZero(pub),
		NoteCancelNotConfirmed(pub, "j"), NoteConfigDrift(pub, []string{"workers.max"}, false), NoteClockSkew(pub, 31), NoteBillingStalled(pub, []string{"b"}),
		NotePreflightFailing(pub, "disk low"), NoteBillingUnreadable(pub, 1), NoteIdleDrift(pub, 90, 20))
	for _, m := range all {
		text := m.Title + "\n" + m.Body + "\n" + m.Click
		for _, a := range m.Actions {
			text += "\n" + a.URL
		}
		for _, bad := range []string{"mmc_", "mmw_", "sig=", "exp=", "token", "Bearer", "rpa_", "?"} {
			if strings.Contains(text, bad) {
				t.Errorf("%q contains %q", m.Title, bad)
			}
		}
	}
}

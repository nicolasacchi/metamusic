// Package notify publishes minimal push notifications (C37).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Message is one notification.
type Message struct {
	Title, Body string
	Priority    int // 1..5
	Tags        []string
	Click       string
	// Actions are ntfy "view" buttons (M3 C82): at most MaxActions, label and URL required. Bodies and
	// URLs never carry tokens, query strings, endpoint ids, keys or refs; callers pass console and page URLs.
	Actions []Action
}

// MaxActions is the most buttons one notification carries.
const MaxActions = 3

// Action is one ntfy button. Only the "view" action (open a URL) is supported.
type Action struct {
	Action string // "view"; empty means "view"
	Label  string
	URL    string
}

// Notifier publishes messages.
type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// NewNtfy returns an ntfy publisher for MM_NTFY_URL (<server base>/<topic>).
// It publishes as JSON to the server base, so the only headers are Content-Type
// and the bearer token: no user-chosen text ever travels as a header. A nil
// client means a 10 s timeout client.
func NewNtfy(url, token string, client *http.Client) Notifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &ntfy{url: url, token: token, client: client}
}

type ntfy struct {
	url, token string
	client     *http.Client
}

const (
	maxTitleRunes   = 200
	maxMessageRunes = 1000
	maxLabelRunes   = 40
)

// clean replaces C0/C1 controls (incl. CR, LF, NUL, U+0085) with spaces and caps the rune count.
func clean(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxRunes {
			break
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

type ntfyPayload struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Actions  []action `json:"actions,omitempty"`
}

type action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
}

// Notify implements Notifier.
func (n *ntfy) Notify(ctx context.Context, m Message) error {
	u, err := neturl.Parse(n.url)
	if err != nil || u.Host == "" {
		return errors.New("ntfy: invalid MM_NTFY_URL")
	}
	topic := path.Base(u.Path)
	if u.Path == "" || topic == "/" || topic == "." {
		return errors.New("ntfy: MM_NTFY_URL has no topic")
	}
	base := *u
	base.Path = path.Dir(u.Path)
	if base.Path == "." {
		base.Path = "/"
	}
	base.RawPath, base.RawQuery, base.Fragment = "", "", ""
	p := ntfyPayload{Topic: topic, Title: clean(m.Title, maxTitleRunes), Message: clean(m.Body, maxMessageRunes)}
	if m.Priority >= 1 && m.Priority <= 5 {
		p.Priority = m.Priority
	}
	for _, t := range m.Tags {
		p.Tags = append(p.Tags, clean(t, 40))
	}
	if m.Click != "" && !strings.ContainsFunc(m.Click, unicode.IsControl) {
		p.Click = m.Click
	}
	for _, a := range m.Actions {
		if len(p.Actions) == MaxActions {
			break
		}
		label := strings.TrimSpace(clean(a.Label, maxLabelRunes))
		if (a.Action != "" && a.Action != "view") || label == "" || a.URL == "" || strings.ContainsFunc(a.URL, unicode.IsControl) {
			continue
		}
		p.Actions = append(p.Actions, action{Action: "view", Label: label, URL: a.URL})
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(raw))
	if err != nil {
		return errors.New("ntfy: building request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *neturl.Error
		if errors.As(err, &ue) {
			err = ue.Err // the *url.Error text carries the full URL
		}
		return fmt.Errorf("ntfy publish: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy publish: status %d", resp.StatusCode)
	}
	return nil
}

// DevicePaired is the notification for a newly paired device.
func DevicePaired(name string) Message {
	return Message{Title: "Device paired", Body: "Device paired: " + name, Priority: 3, Tags: []string{"iphone"}}
}

// TokenCreated is the notification for a new API token; it never carries the token.
func TokenCreated(name string, scopes []string) Message {
	return Message{Title: "Token created", Body: "Token created: " + name + " (" + strings.Join(scopes, ", ") + ")", Priority: 3, Tags: []string{"key"}}
}

// BackupFailed is the notification for a failed backup; code is a stable error code, never raw error text.
func BackupFailed(code string) Message {
	return Message{Title: "Backup failed", Body: "Backup failed: " + code, Priority: 4, Tags: []string{"warning"}}
}

// Async wraps n so Notify returns at once; the publish runs with timeout and
// failures are logged through redact.
func Async(n Notifier, timeout time.Duration) Notifier { return async{n: n, timeout: timeout} }

type async struct {
	n       Notifier
	timeout time.Duration
}

func (a async) Notify(ctx context.Context, m Message) error {
	if a.n == nil {
		return nil
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		c, cancel := context.WithTimeout(bg, a.timeout)
		defer cancel()
		if err := a.n.Notify(c, m); err != nil {
			slog.Warn("notification failed", "err", redact.Error(err))
		}
	}()
	return nil
}

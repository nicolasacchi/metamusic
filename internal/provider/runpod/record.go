package runpod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// FileRecorder writes every client exchange to <dir>/<seq>-<op>.json after redaction (C77.7). It is the
// Recorder behind MM_RUNPOD_RECORD_DIR and the probe/canary fixtures. It never receives an Authorization
// header (the client does not pass one) and drops everything not on the header allowlist.
type FileRecorder struct {
	dir string
	red redact.Redactor

	mu      sync.Mutex
	seq     int
	jobs    map[string]string
	workers map[string]string
	eps     map[string]string // endpoint ids learned from endpoint documents (provision runs without a configured id)
	secrets []string
	err     error
}

// NewFileRecorder records into dir, which must exist. endpointID is replaced by <endpoint> wherever it
// appears; every secret (API keys) is replaced by <key>, in case a server echoes one.
func NewFileRecorder(dir, endpointID string, secrets ...string) (*FileRecorder, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil, errors.New("runpod: record dir must be an existing directory")
	}
	var lits []string
	for _, v := range secrets {
		if len(v) >= 4 {
			lits = append(lits, v)
		}
	}
	return &FileRecorder{dir: dir, secrets: lits, red: redact.WithEndpoint(endpointID), jobs: map[string]string{}, workers: map[string]string{}, eps: map[string]string{}}, nil
}

// Err returns the first error writing a file.
func (r *FileRecorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

var (
	opNameRe = regexp.MustCompile(`[^a-z0-9_]+`)
	runRe    = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
	allowHdr = map[string]bool{"Content-Type": true, "Ratelimit": true, "Ratelimit-Policy": true, "Retry-After": true, "Date": true}
)

type recorded struct {
	Op           string            `json:"op"`
	Method       string            `json:"method"`
	PathTemplate string            `json:"path_template"`
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"`
	RequestBody  any               `json:"request_body"`
	ResponseBody any               `json:"response_body"`
}

// Record implements Recorder.
func (r *FileRecorder) Record(x Exchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, resp := r.parse(x.Request), r.parse(x.Response)
	r.collect(x.Op, resp)
	rec := recorded{Op: x.Op, Method: x.Method, PathTemplate: x.PathTemplate, Status: x.Status, Headers: map[string]string{},
		RequestBody: r.clean("", req), ResponseBody: r.clean("", resp)}
	for k, v := range x.Header {
		if ck := http.CanonicalHeaderKey(k); allowHdr[ck] && len(v) > 0 {
			rec.Headers[strings.ToLower(ck)] = r.scrub(v[0])
		}
	}
	rec.PathTemplate = r.scrub(rec.PathTemplate)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <job-1> readable
	enc.SetIndent("", "  ")
	err := enc.Encode(rec)
	if err == nil {
		r.seq++
		name := fmt.Sprintf("%04d-%s.json", r.seq, opNameRe.ReplaceAllString(x.Op, "_"))
		err = os.WriteFile(filepath.Join(r.dir, name), buf.Bytes(), 0o600)
	}
	if err != nil && r.err == nil {
		r.err = errors.New("runpod: recording an exchange failed")
	}
}

func (r *FileRecorder) parse(b []byte) any {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		s := r.scrub(string(b))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	return v
}

// collect learns job and worker ids so that later occurrences, anywhere, get the same alias.
func (r *FileRecorder) collect(op string, v any) {
	switch x := v.(type) {
	case map[string]any:
		if id, ok := x["id"].(string); ok && id != "" {
			switch op {
			case "run", "status", "cancel":
				r.alias(r.jobs, id, "job")
			case "workers":
				r.alias(r.workers, id, "worker")
			case "list_endpoints", "get_endpoint", "create_endpoint", "patch_endpoint":
				r.eps[id] = "<endpoint>"
			}
		}
		if id, ok := x["workerId"].(string); ok && id != "" {
			r.alias(r.workers, id, "worker")
		}
		for _, e := range x {
			r.collect(op, e)
		}
	case []any:
		for _, e := range x {
			r.collect(op, e)
		}
	}
}

func (r *FileRecorder) alias(m map[string]string, id, kind string) {
	if _, ok := m[id]; !ok {
		m[id] = fmt.Sprintf("<%s-%d>", kind, len(m)+1)
	}
}

// clean redacts a decoded JSON value.
func (r *FileRecorder) clean(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		if key == "input" {
			return map[string]any{"redacted": true}
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.clean(k, e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.clean(key, e)
		}
		return out
	case string:
		return r.cleanString(x)
	}
	return v
}

func (r *FileRecorder) cleanString(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return "<url>"
		}
		switch {
		case strings.Contains(u.Path, "/worker/v1/uploads/"):
			return "<upload-url>"
		case strings.Contains(u.Path, "/worker/v1/attempts/"):
			return "<start-url>"
		}
		return r.scrub(u.Path)
	}
	return r.scrub(s)
}

// scrub applies the known aliases, the endpoint literal, the generic credential masks and the long-run rule.
func (r *FileRecorder) scrub(s string) string {
	for _, k := range r.secrets {
		s = strings.ReplaceAll(s, k, "<key>")
	}
	for id, a := range r.jobs {
		s = strings.ReplaceAll(s, id, a)
	}
	for id, a := range r.workers {
		s = strings.ReplaceAll(s, id, a)
	}
	for id, a := range r.eps {
		s = strings.ReplaceAll(s, id, a)
	}
	s = r.red.String(s)
	return runRe.ReplaceAllString(s, "<redacted>")
}

package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestRedact_PodPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"get pod", `Get "https://api.runpod.io/v2/pods/7h9k2m4n6p": EOF`, `Get "https://api.runpod.io/v2/pods/<pod>": EOF`},
		{"pod logs with query", "GET /v2/pods/7h9k2m4n6p/logs?source=container&tail=50 failed", "GET /v2/pods/<pod>/logs?<redacted> failed"},
		{"pod action", "POST /v2/pods/7h9k2m4n6p/action 409", "POST /v2/pods/<pod>/action 409"},
		{"delete queued", "DELETE /v2/queued-pods/q8x2m4n6pw: timeout", "DELETE /v2/queued-pods/<pod>: timeout"},
		{"poll route", "POST /worker/v1/pods/7h9k2m4n6p/poll?exp=1760018552&sig=abc", "POST /worker/v1/pods/<pod>/poll?<redacted>"},
		{"list is kept", "GET /v2/pods?limit=1000", "GET /v2/pods?<redacted>"},
		{"two ids", "/v2/pods/aaaa1111bb then /v2/pods/cccc2222dd", "/v2/pods/<pod> then /v2/pods/<pod>"},
		{"serverless rules still apply", "GET /v2/endpoints/abc123def", "GET /v2/endpoints/<endpoint>"},
		{"unrelated kept", "pods are named mm-0190abcd1234", "pods are named mm-0190abcd1234"},
	} {
		if got := String(tc.in); got != tc.want {
			t.Errorf("%s: String(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	// the redactor and Error use the same rules
	r := WithEndpoint("ep-secret-0001")
	if got := r.Error(errors.New("DELETE /v2/pods/7h9k2m4n6p on ep-secret-0001")); strings.Contains(got, "7h9k2m4n6p") || strings.Contains(got, "ep-secret-0001") {
		t.Errorf("Redactor.Error leaked: %q", got)
	}
	if got := Error(errors.New("GET /v2/pods/7h9k2m4n6p")); got != "GET /v2/pods/<pod>" {
		t.Errorf("Error = %q", got)
	}
}

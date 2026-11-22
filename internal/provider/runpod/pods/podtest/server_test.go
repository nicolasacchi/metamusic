package podtest_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
)

func post(t *testing.T, srv *podtest.Server, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v2/pods", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFake_RejectsWhatTheContractRejects(t *testing.T) {
	t.Parallel()
	srv := podtest.New(t, podtest.Options{Key: "k-for-the-fake"})
	good := `{"name":"mm-0190abcd1234","image":"img@sha256:00","gpu":{"id":"NVIDIA GeForce RTX 4090","count":1}}`
	for name, tc := range map[string]struct {
		tok, body string
		want      int
	}{
		"no token":             {"", good, 401},
		"wrong token":          {"other", good, 401},
		"good":                 {"k-for-the-fake", good, 201},
		"queue is in the spec": {"k-for-the-fake", `{"name":"a","image":"i","gpu":{"id":"NVIDIA L40S"},"queue":{}}`, 201},
		"v1 name":              {"k-for-the-fake", `{"name":"a","image":"i","gpuTypeIds":["x"]}`, 422},
		"unknown gpu key":      {"k-for-the-fake", `{"name":"a","image":"i","gpu":{"id":"NVIDIA L40S","type":"x"}}`, 422},
		"no name":              {"k-for-the-fake", `{"image":"i","gpu":{"id":"NVIDIA L40S"}}`, 422},
		"no image":             {"k-for-the-fake", `{"name":"a","gpu":{"id":"NVIDIA L40S"}}`, 422},
		"no gpu":               {"k-for-the-fake", `{"name":"a","image":"i"}`, 422},
		"gpu and cpu":          {"k-for-the-fake", `{"name":"a","image":"i","gpu":{"id":"x"},"cpu":{}}`, 422},
		"not json":             {"k-for-the-fake", `{`, 422},
		"unknown type":         {"k-for-the-fake", `{"name":"a","image":"i","gpu":{"id":"NVIDIA Nope"}}`, 400},
		"too large":            {"k-for-the-fake", `{"name":"` + strings.Repeat("a", 103000) + `"}`, 413},
	} {
		if got, b := post(t, srv, tc.tok, tc.body); got != tc.want {
			t.Errorf("%s: status %d (%s), want %d", name, got, b, tc.want)
		}
	}
	if n := len(srv.Pods()); n != 2 {
		t.Errorf("pods = %d, want 2 (the good body and the one with a spec property)", n)
	}
	if got := srv.Calls(podtest.OpCreate); got != 13 {
		t.Errorf("calls = %d", got)
	}
}

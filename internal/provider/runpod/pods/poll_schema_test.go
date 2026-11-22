package pods_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

func loadPollSchema(t *testing.T, name string) (*jsonschema.Resolved, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "worker", "src", "mmworker", "schema", name))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	return rs, generic
}

// jsonKeys lists the JSON names of a struct's fields.
func jsonKeys(v any) []string {
	var keys []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		keys = append(keys, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
	}
	slices.Sort(keys)
	return keys
}

func schemaKeys(t *testing.T, node map[string]any, field string) []string {
	t.Helper()
	var keys []string
	switch f := node[field].(type) {
	case map[string]any:
		for k := range f {
			keys = append(keys, k)
		}
	case []any:
		for _, k := range f {
			keys = append(keys, k.(string))
		}
	default:
		t.Fatalf("no %s", field)
	}
	slices.Sort(keys)
	return keys
}

// objectOf returns the object schema of a property that is an object, or null-or-object.
func objectOf(t *testing.T, props map[string]any, name string) map[string]any {
	t.Helper()
	n := props[name].(map[string]any)
	if alts, ok := n["oneOf"].([]any); ok {
		for _, a := range alts {
			if m := a.(map[string]any); m["type"] == "object" {
				return m
			}
		}
	}
	return n
}

// TestPollSchemas_MatchGo: the Go structs and the two schema files carry the same property names, the same required lists and the same
// enums; documents built by Go validate, and the schemas refuse what the handler refuses.
func TestPollSchemas_MatchGo(t *testing.T) {
	t.Parallel()
	req, reqGeneric := loadPollSchema(t, "mm-pod-poll-1.request.schema.json")
	resp, respGeneric := loadPollSchema(t, "mm-pod-poll-1.response.schema.json")
	for name, g := range map[string]map[string]any{"request": reqGeneric, "response": respGeneric} {
		if g["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties is not false", name)
		}
	}
	type pair struct {
		schema map[string]any
		goType any
	}
	rprops := reqGeneric["properties"].(map[string]any)
	for name, p := range map[string]pair{
		"request":  {reqGeneric, pods.PollRequest{}},
		"agent":    {rprops["agent"].(map[string]any), pods.PollAgent{}},
		"boot":     {objectOf(t, rprops, "boot"), pods.PollBoot{}},
		"done":     {objectOf(t, rprops, "done"), pods.PollDone{}},
		"response": {respGeneric, pods.PollResponse{}},
	} {
		if got, want := schemaKeys(t, p.schema, "properties"), jsonKeys(p.goType); !reflect.DeepEqual(got, want) {
			t.Errorf("%s properties: schema %v, Go %v", name, got, want)
		}
		if got, want := schemaKeys(t, p.schema, "required"), jsonKeys(p.goType); !reflect.DeepEqual(got, want) {
			t.Errorf("%s required: schema %v, Go %v", name, got, want)
		}
	}
	enum := func(m map[string]any, key string) []string {
		var out []string
		for _, v := range m["properties"].(map[string]any)[key].(map[string]any)["enum"].([]any) {
			out = append(out, v.(string))
		}
		slices.Sort(out)
		return out
	}
	if got := enum(reqGeneric, "state"); !reflect.DeepEqual(got, []string{"booting", "busy", "idle"}) {
		t.Errorf("state enum %v", got)
	}
	if got := enum(objectOf(t, rprops, "done"), "status"); !reflect.DeepEqual(got, []string{"error", "ok"}) {
		t.Errorf("status enum %v", got)
	}
	if got := enum(respGeneric, "action"); !reflect.DeepEqual(got, []string{pods.ActionStop, pods.ActionTake, pods.ActionWait}) {
		t.Errorf("action enum %v", got)
	}

	validate := func(rs *jsonschema.Resolved, doc any) error {
		var inst any
		if err := json.Unmarshal(mustJSON(t, doc), &inst); err != nil {
			t.Fatal(err)
		}
		return rs.Validate(inst)
	}
	// Go-built documents validate
	code := "OUT_OF_MEMORY"
	for _, ok := range []pods.PollRequest{
		{State: "booting", Agent: pods.PollAgent{Version: "1", Build: "b"}},
		{State: "idle", Agent: pods.PollAgent{Version: "1", Build: "b"}, Boot: &pods.PollBoot{GPUName: "g", CUDA: "12.8", WeightsReady: true, LoadS: 41}},
		{State: "busy", Agent: pods.PollAgent{Version: "1", Build: "b"}, AttemptID: ptr("01900000-0000-7000-8000-0000000c0001"),
			Done: &pods.PollDone{AttemptID: "01900000-0000-7000-8000-0000000c0001", Status: "error", ExecutionMS: 5, ErrorCode: &code}},
	} {
		if err := validate(req, ok); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for _, ok := range []pods.PollResponse{
		{Action: "wait", WaitS: 20, Cancel: []string{}, DeadlineUnix: 1},
		{Action: "stop", Cancel: []string{}, DeadlineUnix: 1},
		{Action: "take", Take: json.RawMessage(`{"contract":"mm.worker/1"}`), Cancel: []string{"01900000-0000-7000-8000-0000000c0001"}, DeadlineUnix: 1},
	} {
		if err := validate(resp, ok); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	// the schemas refuse what the handler refuses (the handler's cases are TestPodPoll_Matrix)
	good := idle()
	for name, bad := range map[string]pollIn{
		"unknown key":   good.with("extra", 1),
		"bad state":     good.with("state", "sleeping"),
		"bad attempt":   good.with("attempt_id", "nope"),
		"bad done":      good.with("done", done("01900000-0000-7000-8000-0000000c0001", "maybe", 1)),
		"negative ms":   good.with("done", done("01900000-0000-7000-8000-0000000c0001", "ok", -1)),
		"empty version": good.with("agent", map[string]any{"version": "", "build": "b"}),
		"nested extra":  good.with("agent", map[string]any{"version": "1", "build": "b", "x": 1}),
	} {
		if validate(req, bad) == nil {
			t.Errorf("request schema accepted %s", name)
		}
	}
	if validate(req, map[string]any{"state": "idle", "agent": map[string]any{"version": "1", "build": "b"}}) == nil {
		t.Error("request schema accepted a body without the nullable keys")
	}
	for name, bad := range map[string]map[string]any{
		"take with wait": {"action": "wait", "wait_s": 1, "take": map[string]any{"a": 1}, "cancel": []string{}, "deadline_unix": 1},
		"take null":      {"action": "take", "wait_s": 0, "take": nil, "cancel": []string{}, "deadline_unix": 1},
		"null cancel":    {"action": "wait", "wait_s": 1, "take": nil, "cancel": nil, "deadline_unix": 1},
		"bad action":     {"action": "go", "wait_s": 1, "take": nil, "cancel": []string{}, "deadline_unix": 1},
		"extra":          {"action": "wait", "wait_s": 1, "take": nil, "cancel": []string{}, "deadline_unix": 1, "x": 1},
	} {
		if validate(resp, bad) == nil {
			t.Errorf("response schema accepted %s", name)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestPodPoll_ResponsesValidateAgainstSchema: every answer of the handler (wait, take, stop) validates against the response schema.
func TestPodPoll_ResponsesValidateAgainstSchema(t *testing.T) {
	t.Parallel()
	rs, _ := loadPollSchema(t, "mm-pod-poll-1.response.schema.json")
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	r.queueTake(rigBatch, 200)
	r.take(rigBatch) // keeps the batch live after the first is handed
	check := func(w interface{ String() string }, body []byte) {
		t.Helper()
		var inst any
		if err := json.Unmarshal(body, &inst); err != nil {
			t.Fatal(err)
		}
		if err := rs.Validate(inst); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
	for _, in := range []pollIn{idle(), idle(), state("booting")} { // take, wait, wait (a restart report)
		w := r.poll(r.link(rigPod), in)
		check(w.Body, w.Body.Bytes())
	}
	r.setState(rigPod, "terminating")
	w := r.poll(r.link(rigPod), idle())
	check(w.Body, w.Body.Bytes())
}

// TestPodPoll_NotInRegistry: the poll route is not a Huma operation: the committed OpenAPI document (generated from the registry, which
// also derives the MCP tools and the SPA client) has no path or operation id for it or for any worker route.
func TestPodPoll_NotInRegistry(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Paths) < 20 {
		t.Fatalf("only %d paths: the test reads the wrong file", len(doc.Paths))
	}
	for path, item := range doc.Paths {
		if strings.Contains(path, "/worker") || strings.Contains(path, "poll") {
			t.Errorf("OpenAPI path %s", path)
		}
		for _, op := range item {
			if strings.Contains(op.OperationID, "pod_poll") || strings.Contains(op.OperationID, "poll_pod") {
				t.Errorf("operation %s", op.OperationID)
			}
		}
	}
	for _, leak := range []string{"mm-pod-poll", "POD_LINK", "pod_poll"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("openapi.json mentions %s", leak)
		}
	}
}

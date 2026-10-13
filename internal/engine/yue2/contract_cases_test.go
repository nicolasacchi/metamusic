package yue2

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The worker-contract job (worker/contract/test_yue2_contract.py, C83.4) feeds every line of
// batch_cases.jsonl to yue2's own request validation and compares accepted/rejected with
// batch_cases.expect.json, which is Go's ModeYuE2 verdict (ValidateBatch). That closes the "batch-validator
// drift" risk of M2a. The rows are the single-line requests of TestValidateBatch_Table (batch_test.go,
// owned by the engine lane and not editable here) plus further single-line shapes; rows needing a
// directory (abc_path) are excluded because the Python side has no file tree.

var batchContractRows = []struct{ name, line string }{
	{"valid minimal", `{"id":"a","style":"s","lyrics":"l"}`},
	{"valid full", `{"id":"a","style":"s","lyrics":"l","cot":"melody","seed":7,"abc":"X:1","cfg_scale":1.5}`},
	{"valid cot off", `{"id":"a","style":"s","lyrics":"l","cot":"off","seed":0}`},
	{"valid metadata", `{"id":"a","style":"s","lyrics":"l","lang":"en","eval_index":3,"clip_id":"c"}`},
	{"valid tags alias", `{"id":"a","tags":"s","lyrics":"l"}`},
	{"CRLF residue", `{"id":"a","style":"s","lyrics":"l"}` + "\r"},
	{"unknown key fatal", `{"id":"a","style":"s","lyrics":"l","title":"t"}`},
	{"bad json", `{not json`},
	{"not object", `[1]`},
	{"duplicate keys last wins", `{"id":"a","id":"b","style":"s","lyrics":"l"}`},
	{"missing lyrics", `{"id":"a","style":"s"}`},
	{"missing style", `{"id":"a","lyrics":"l"}`},
	{"bad cot", `{"id":"a","style":"s","lyrics":"l","cot":"half"}`},
	{"seed negative", `{"id":"a","style":"s","lyrics":"l","seed":-1}`},
	{"seed string", `{"id":"a","style":"s","lyrics":"l","seed":"1"}`},
	{"seed bool", `{"id":"a","style":"s","lyrics":"l","seed":true}`},
	{"id unsafe", `{"id":"../x","style":"s","lyrics":"l"}`},
	{"cfg_scale too high", `{"id":"a","style":"s","lyrics":"l","cfg_scale":21}`},
	{"abc with cot off", `{"id":"a","style":"s","lyrics":"l","cot":"off","abc":"X:1"}`},
	{"abc and abc_path", `{"id":"a","style":"s","lyrics":"l","abc":"X:1","abc_path":"a.abc"}`},
}

type batchExpectCase struct {
	Name   string `json:"name"`
	Accept bool   `json:"accept"`
}

type batchExpect struct {
	Source string            `json:"source"`
	Cases  []batchExpectCase `json:"cases"`
}

// renderBatchContractCases regenerates both committed files in memory.
func renderBatchContractCases(t *testing.T) (jsonl, expect []byte) {
	t.Helper()
	var jl bytes.Buffer
	exp := batchExpect{Source: "Go ValidateBatch (ModeYuE2); regenerate with go test ./internal/engine/yue2 -run TestWriteBatchContractCases -update"}
	for _, r := range batchContractRows {
		if strings.Contains(strings.TrimRight(r.line, "\r"), "\n") {
			t.Fatalf("row %q is not a single line", r.name)
		}
		jl.WriteString(r.line + "\n")
		rep := ValidateBatch([]byte(r.line+"\n"), nil)
		exp.Cases = append(exp.Cases, batchExpectCase{Name: r.name, Accept: rep.OK})
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return jl.Bytes(), append(b, '\n')
}

func contractTestdata() string {
	return filepath.Join("..", "..", "..", "worker", "contract", "testdata")
}

// staleFiles returns the names whose content in dir differs from want (or is missing).
func staleFiles(dir string, want map[string][]byte) []string {
	var out []string
	for name, w := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, w) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func renderedFiles(t *testing.T) map[string][]byte {
	jsonl, expect := renderBatchContractCases(t)
	return map[string][]byte{"batch_cases.jsonl": jsonl, "batch_cases.expect.json": expect}
}

// TestWriteBatchContractCases writes the files with -update and otherwise fails when they are stale.
func TestWriteBatchContractCases(t *testing.T) {
	t.Parallel()
	files := renderedFiles(t)
	dir := contractTestdata()
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if stale := staleFiles(dir, files); len(stale) > 0 {
		t.Errorf("stale or missing %v; run go test ./internal/engine/yue2 -run TestWriteBatchContractCases -update", stale)
	}
}

// TestWriteBatchContractCases_Stale: a committed file that differs from the regeneration is reported, an
// identical one is not, and the verdict file holds both accepted and rejected cases.
func TestWriteBatchContractCases_Stale(t *testing.T) {
	t.Parallel()
	files := renderedFiles(t)
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if s := staleFiles(dir, files); len(s) != 0 {
		t.Fatalf("fresh files reported stale: %v", s)
	}
	if err := os.WriteFile(filepath.Join(dir, "batch_cases.expect.json"),
		bytes.Replace(files["batch_cases.expect.json"], []byte(`"accept": true`), []byte(`"accept": false`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := staleFiles(dir, files); !slices.Equal(s, []string{"batch_cases.expect.json"}) {
		t.Fatalf("stale = %v", s)
	}
	var e batchExpect
	if err := json.Unmarshal(files["batch_cases.expect.json"], &e); err != nil {
		t.Fatal(err)
	}
	var yes, no int
	for _, c := range e.Cases {
		if c.Accept {
			yes++
		} else {
			no++
		}
	}
	if lines := bytes.Count(files["batch_cases.jsonl"], []byte("\n")); yes == 0 || no == 0 || len(e.Cases) != lines {
		t.Fatalf("accept=%d reject=%d cases=%d lines=%d", yes, no, len(e.Cases), lines)
	}
}

// TestArtifacts_MatchWorkerContract: the artifacts recorded from real GPU renders (provider's
// artifacts.observed.json; the placeholder {} until M3-X3) are a subset, per cot, of what the pinned
// yue2 save_artifacts writes (worker/contract/testdata/artifacts.json), every observed name is on the
// worker's upload allowlist (the input schema), and latent.npy is on neither the allowlist nor the
// observed set (design 3.3, D-M1-1).
func TestArtifacts_MatchWorkerContract(t *testing.T) {
	t.Parallel()
	read := func(path string, into any) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	var contract, observed map[string][]string
	read(filepath.Join(contractTestdata(), "artifacts.json"), &contract)
	read(filepath.Join("..", "..", "provider", "artifacts.observed.json"), &observed)
	var schema struct {
		Properties struct {
			Uploads struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"uploads"`
		} `json:"properties"`
	}
	read(filepath.Join("..", "..", "..", "worker", "src", "mmworker", "schema", "mm-worker-1.input.schema.json"), &schema)
	allow := schema.Properties.Uploads.Properties
	if len(allow) == 0 {
		t.Fatal("empty upload allowlist in the input schema")
	}
	if _, bad := allow["latent.npy"]; bad {
		t.Error("latent.npy is allowlisted")
	}
	for _, cot := range []string{"full", "melody", "off"} {
		if len(contract[cot]) == 0 {
			t.Errorf("artifacts.json has no entry for cot %s", cot)
		}
	}
	for cot, names := range observed {
		for _, n := range names {
			if n == "latent.npy" {
				t.Errorf("observed[%s] lists latent.npy", cot)
			}
			if _, ok := allow[n]; !ok {
				t.Errorf("observed[%s] lists %s, which is not allowlisted", cot, n)
			}
			if !slices.Contains(contract[cot], n) {
				t.Errorf("observed[%s] lists %s, which yue2 save_artifacts does not write for that cot", cot, n)
			}
		}
	}
}

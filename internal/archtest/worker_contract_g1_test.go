package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// contractWriteRE matches a SQL write to worker_contract_log.
var contractWriteRE = regexp.MustCompile(`(?i)\b(?:insert\s+(?:or\s+\w+\s+)?into|replace\s+into|update|delete\s+from)\s+worker_contract_log\b`)

// contractOpRE matches an operation id that would put the capability gate on the API.
var contractOpRE = regexp.MustCompile(`OperationID:\s*"[a-z_]*(?:contract|worker_caps)[a-z_]*"`)

func contractOffenders(files map[string]string) (off []string) {
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if helperSubtree(dir) || inDir(path, "internal/store/migrations") {
			continue
		}
		code := stripLineComments(src)
		if contractWriteRE.MatchString(code) && path != "internal/provider/workercaps.go" {
			off = append(off, path+": writes worker_contract_log outside provider.AppendContract")
		}
		if (inDir(path, "internal/api") || inDir(path, "internal/mcpgen") || inDir(path, "internal/httpserver")) && contractOpRE.MatchString(code) {
			off = append(off, path+": registers an operation for the worker capability gate")
		}
	}
	sort.Strings(off)
	return off
}

// TestWorkerContractWriters: the capability gate is opened only by the operator CLI and closed only by the CLI or the runner's
// settle (C322.5, C322.6), and both write through provider.AppendContract; no API operation, MCP tool or SPA route can open a
// capability, so an agent can never reach it. Test helpers and the migration are exempt.
func TestWorkerContractWriters(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	if _, ok := all["internal/provider/workercaps.go"]; !ok {
		t.Fatal("internal/provider/workercaps.go not scanned")
	}
	for _, o := range contractOffenders(all) {
		t.Errorf("C322 violated: %s", o)
	}
	got := contractOffenders(map[string]string{
		"internal/provider/workercaps.go":     "INSERT INTO worker_contract_log(a)",
		"internal/jobs/x.go":                  "UPDATE worker_contract_log SET a = 1",
		"internal/api/y.go":                   `OperationID: "open_worker_contract"`,
		"internal/servertest/z.go":            "INSERT INTO worker_contract_log(a)", // helper subtree
		"internal/store/migrations/gen.go":    "INSERT INTO worker_contract_log(a)",
		"cmd/metamusic/cmd_runpod_ok.go":      "// INSERT INTO worker_contract_log in a comment",
		"internal/api/ops_provider_m3.go":     `OperationID: "get_provider_status"`,
		"internal/generation/reads_worker.go": "SELECT * FROM worker_contract_log",
	})
	if len(got) != 2 {
		t.Errorf("planted offenders = %v, want the jobs write and the api operation", got)
	}
}

//go:build ignore

// gen-jobs-ddl prints (or, with -write, splices into a migration file) the jobs rebuild block of a migration: the CREATE
// TABLE of the jobs table as it stands after every migration numbered below -before, with the edits of the named profile
// applied, an INSERT that names every column of the old table in order (never SELECT *), DROP, RENAME, and every index and
// trigger on jobs re-created verbatim from sqlite_master (a profile may edit a trigger too).
//
// Profiles (G1 C323.2, D-G1-13), each a table of exact-text edits that must match exactly once:
//
//	engines    migration 15: expected_audio_s BETWEEN 1 AND 360 becomes 600; nothing else
//	job_kinds  migration 20: kind CHECK, needs_json and spec_json, the completion CHECK relaxed for plan jobs, two kind
//	           CHECKs, and jobs_frozen lists the new columns
//
//	go run scripts/gen-jobs-ddl.go -profile job_kinds                print the block
//	go run scripts/gen-jobs-ddl.go -profile job_kinds -write internal/store/migrations/0020_job_kinds.sql
//	go run scripts/gen-jobs-ddl.go -profile job_kinds -check internal/store/migrations/0020_job_kinds.sql
//	go run scripts/gen-jobs-ddl.go -check-all                        -check for every profile at its default file
//
// -before overrides the profile's migration number (a lane renumbers on rebase by changing the profile table). The
// migration holds the block between the BEGIN/END markers below; -check fails when the file is stale.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	beginMark = "-- BEGIN GENERATED jobs rebuild (scripts/gen-jobs-ddl.go)"
	endMark   = "-- END GENERATED jobs rebuild"
)

// edit replaces old by new in the DDL (or the named object's SQL); old must occur exactly once.
type edit struct{ old, new string }

// profile is one generated rebuild.
type profile struct {
	before int    // the migration number: the schema is read after migrations numbered below it
	file   string // the migration file that holds the block (default for -check-all)
	// table edits are applied to the CREATE TABLE text (after the jobs_new rename of its first line).
	table []edit
	// triggers edits are applied to the SQL of the re-created indexes and triggers.
	triggers []edit
}

var profiles = map[string]profile{
	"engines": {
		before: 15, file: "0015_engines.sql",
		table: []edit{{
			"expected_audio_s    INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 360)",
			"expected_audio_s    INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 600)",
		}},
	},
	"job_kinds": {
		before: 20, file: "0020_job_kinds.sql",
		table: []edit{
			{"kind                TEXT NOT NULL,                     -- Go-validated: 'generate' (D-M3-15)",
				"kind                TEXT NOT NULL CHECK (kind IN ('generate','analyze','plan')),"},
			{"updated_at          TEXT NOT NULL, patch_id TEXT REFERENCES patches(id),\n  UNIQUE (batch_id, take_index),",
				"updated_at          TEXT NOT NULL, patch_id TEXT REFERENCES patches(id),\n" +
					"  needs_json          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(needs_json) AND json_type(needs_json) = 'array'),\n" +
					"  spec_json           TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(spec_json) AND json_type(spec_json) = 'object'),\n" +
					"  UNIQUE (batch_id, take_index),"},
			{"  CHECK (state <> 'completed' OR track_id IS NOT NULL),",
				"  CHECK (state <> 'completed' OR track_id IS NOT NULL OR kind = 'plan'),\n" +
					"  CHECK (kind <> 'plan' OR track_id IS NULL),\n" +
					"  CHECK (kind <> 'analyze' OR source_track_id IS NOT NULL),"},
		},
		triggers: []edit{{"expected_micro_usd, reserved_micro_usd, created_at ON jobs",
			"expected_micro_usd, reserved_micro_usd, created_at, needs_json, spec_json ON jobs"}},
	},
}

func profileNames() []string {
	var ns []string
	for n := range profiles {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}

func applyEdits(what, text string, es []edit) (string, error) {
	for _, e := range es {
		if strings.Count(text, e.old) != 1 {
			return "", fmt.Errorf("%s: edit target not found exactly once: %q", what, firstLine(e.old))
		}
		text = strings.Replace(text, e.old, e.new, 1)
	}
	return text, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func main() {
	dir := flag.String("migrations", "internal/store/migrations", "migrations directory")
	before := flag.Int("before", 0, "apply migrations numbered below this one (default: the profile's)")
	prof := flag.String("profile", "", "profile: "+strings.Join(profileNames(), "|"))
	write := flag.String("write", "", "splice the block into this migration file")
	check := flag.String("check", "", "fail when this migration file's block differs")
	checkAll := flag.Bool("check-all", false, "check every profile against its migration file under -migrations")
	flag.Parse()
	if *checkAll {
		for _, n := range profileNames() {
			p := profiles[n]
			path := filepath.Join(*dir, p.file)
			if err := checkFile(*dir, n, p, p.before, path); err != nil {
				fatal(n + ": " + err.Error())
			}
		}
		return
	}
	p, ok := profiles[*prof]
	if !ok {
		fatal("-profile is required: " + strings.Join(profileNames(), ", "))
	}
	if *before == 0 {
		*before = p.before
	}
	if *before < 2 {
		fatal("-before is required")
	}
	block, err := generate(*dir, *before, p)
	if err != nil {
		fatal(err.Error())
	}
	switch {
	case *write != "":
		out, err := splice(*write, block)
		if err != nil {
			fatal(err.Error())
		}
		if err := os.WriteFile(*write, []byte(out), 0o644); err != nil {
			fatal(err.Error())
		}
	case *check != "":
		if err := checkFile(*dir, *prof, p, *before, *check); err != nil {
			fatal(err.Error())
		}
	default:
		fmt.Print(block)
	}
}

func checkFile(dir, name string, p profile, before int, path string) error {
	block, err := generate(dir, before, p)
	if err != nil {
		return err
	}
	out, err := splice(path, block)
	if err != nil {
		return err
	}
	cur, _ := os.ReadFile(path)
	if string(cur) != out {
		return fmt.Errorf("%s: generated jobs block is stale (profile %s); run with -write", path, name)
	}
	return nil
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "gen-jobs-ddl:", msg)
	os.Exit(1)
}

func splice(path, block string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := string(b)
	i, j := strings.Index(s, beginMark), strings.Index(s, endMark)
	if i < 0 || j < i {
		return "", fmt.Errorf("%s: markers missing", path)
	}
	return s[:i] + block + s[j:], nil
}

func generate(dir string, before int, p profile) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return "", err
	}
	type mig struct {
		n    int
		path string
	}
	var ms []mig
	for _, f := range files {
		numStr, _, _ := strings.Cut(filepath.Base(f), "_")
		n, err := strconv.Atoi(numStr)
		if err != nil {
			return "", fmt.Errorf("bad migration name %s", f)
		}
		if n < before {
			ms = append(ms, mig{n, f})
		}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].n < ms[j].n })
	if len(ms) != before-1 {
		return "", fmt.Errorf("found %d migrations below %d, want %d (gap?)", len(ms), before, before-1)
	}
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, m := range ms {
		b, err := os.ReadFile(m.path)
		if err != nil {
			return "", err
		}
		if _, err := db.Exec(string(b)); err != nil {
			return "", fmt.Errorf("%s: %w", m.path, err)
		}
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&ddl); err != nil {
		return "", err
	}
	if ddl, err = applyEdits("jobs DDL", ddl, p.table); err != nil {
		return "", err
	}
	// a table renamed by an earlier rebuild keeps the quoted name SQLite wrote at the rename
	rest, found := strings.CutPrefix(ddl, "CREATE TABLE jobs (")
	if !found {
		rest, found = strings.CutPrefix(ddl, `CREATE TABLE "jobs" (`)
	}
	if !found {
		return "", fmt.Errorf("unexpected jobs DDL prefix")
	}
	ddl = "CREATE TABLE jobs_new (" + rest

	rows, err := db.Query(`SELECT name FROM pragma_table_info('jobs') ORDER BY cid`)
	if err != nil {
		return "", err
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		cols = append(cols, c)
	}
	rows.Close()
	list := strings.Join(cols, ", ")

	var sb strings.Builder
	sb.WriteString(beginMark + "\n")
	fmt.Fprintf(&sb, "-- Generated from the schema after migration %d; do not edit by hand (go run scripts/gen-jobs-ddl.go -check).\n", before-1)
	sb.WriteString(ddl + ";\n")
	fmt.Fprintf(&sb, "INSERT INTO jobs_new (%s)\n  SELECT %s FROM jobs;\n", list, list)
	sb.WriteString("DROP TABLE jobs;\nALTER TABLE jobs_new RENAME TO jobs;\n")
	objs, err := db.Query(`SELECT sql FROM sqlite_master WHERE tbl_name='jobs' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return "", err
	}
	defer objs.Close()
	for objs.Next() {
		var s string
		if err := objs.Scan(&s); err != nil {
			return "", err
		}
		sb.WriteString(s + ";\n")
	}
	if err := objs.Err(); err != nil {
		return "", err
	}
	out := sb.String()
	if len(p.triggers) > 0 {
		if out, err = applyEdits("jobs triggers", out, p.triggers); err != nil {
			return "", err
		}
	}
	return out, nil
}

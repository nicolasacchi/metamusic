//go:build ignore

// gen-jobs-ddl prints (or, with -write, splices into a migration file) the jobs rebuild block of the engines
// migration: the CREATE TABLE of the jobs table as it stands after every migration numbered below -before, with the
// one CHECK on expected_audio_s widened from 360 to 600 s, an INSERT that names every column in table order (never
// SELECT *), DROP, RENAME, and every index and trigger on jobs re-created verbatim from sqlite_master.
//
//	go run scripts/gen-jobs-ddl.go -before 14                        print the block
//	go run scripts/gen-jobs-ddl.go -before 14 -write internal/store/migrations/0014_engines.sql
//	go run scripts/gen-jobs-ddl.go -before 14 -check internal/store/migrations/0014_engines.sql
//
// The migration holds the block between the BEGIN/END markers below; -check fails when the file is stale.
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
	oldCheck  = "expected_audio_s    INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 360)"
	newCheck  = "expected_audio_s    INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 600)"
)

func main() {
	dir := flag.String("migrations", "internal/store/migrations", "migrations directory")
	before := flag.Int("before", 0, "apply migrations numbered below this one")
	write := flag.String("write", "", "splice the block into this migration file")
	check := flag.String("check", "", "fail when this migration file's block differs")
	flag.Parse()
	if *before < 2 {
		fatal("-before is required")
	}
	block, err := generate(*dir, *before)
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
		out, err := splice(*check, block)
		if err != nil {
			fatal(err.Error())
		}
		cur, _ := os.ReadFile(*check)
		if string(cur) != out {
			fatal(*check + ": generated jobs block is stale; run with -write")
		}
	default:
		fmt.Print(block)
	}
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

func generate(dir string, before int) (string, error) {
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
	if strings.Count(ddl, oldCheck) != 1 {
		return "", fmt.Errorf("the expected_audio_s CHECK was not found exactly once")
	}
	ddl = strings.Replace(ddl, oldCheck, newCheck, 1)
	if !strings.HasPrefix(ddl, "CREATE TABLE jobs (") {
		return "", fmt.Errorf("unexpected jobs DDL prefix")
	}
	ddl = "CREATE TABLE jobs_new (" + strings.TrimPrefix(ddl, "CREATE TABLE jobs (")

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
	return sb.String(), objs.Err()
}

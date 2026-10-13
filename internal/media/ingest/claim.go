package ingest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// beforeClaim is a test hook called just before step 14.
var beforeClaim func(trackID string)

// safeName reports whether s is usable as one path component we create.
func safeName(s string) bool {
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, ".") || len(s) > 200 {
		return false
	}
	return !strings.ContainsAny(s, "/\\\x00")
}

// claimer hard-links finished files from the work dir into their final names.
type claimer struct {
	data    *os.Root
	created []string
}

// link claims to as a hard link of from. exists reports EEXIST (nothing replaced).
func (c *claimer) link(from, to string) (exists bool, err error) {
	if err := c.data.MkdirAll(path.Dir(to), 0o755); err != nil {
		return false, errorf(CodeIO, path.Dir(to), "create directory")
	}
	if err := c.data.Link(from, to); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return true, nil
		}
		return false, &Error{Code: CodeIO, Path: to, Detail: "link into place", Err: err}
	}
	c.created = append(c.created, to)
	return false, nil
}

// masterName is audio/masters/<slug>__<seed>__<yyyymmdd-hhmm>[__N].<ext> (ext "flac" or "mp3").
func masterName(slug string, seed int64, at time.Time, n int, ext string) string {
	base := fmt.Sprintf("%s__%d__%s", slug, seed, at.UTC().Format("20060102-1504"))
	if n > 1 {
		base += fmt.Sprintf("__%d", n)
	}
	return "audio/masters/" + base + "." + ext
}

// claimMaster finds the first free master name (up to __99).
func (c *claimer) claimMaster(from, slug string, seed int64, at time.Time, ext string) (string, error) {
	for n := 1; n <= 99; n++ {
		name := masterName(slug, seed, at, n, ext)
		exists, err := c.link(from, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
	return "", errorf(CodeIO, masterName(slug, seed, at, 1, ext), "no free master name")
}

// Discard removes exactly the master and takes/<TrackID>/ files listed in
// r.Created, plus the then-empty takes directory. It never removes derived/
// files (content-addressed, possibly shared) and never touches a path not in
// Created.
func (p *Pipeline) Discard(r Result) error {
	return discard(p.data, r.Created)
}

func discard(data *os.Root, created []string) error {
	var first error
	dirs := map[string]bool{}
	for _, rel := range created {
		clean := path.Clean(rel)
		switch {
		case clean != rel:
			continue
		case strings.HasPrefix(rel, "audio/masters/") && strings.Count(rel, "/") == 2:
		case strings.HasPrefix(rel, "takes/") && strings.Count(rel, "/") == 2:
			dirs[path.Dir(rel)] = true
		default:
			continue
		}
		if err := data.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = &Error{Code: CodeIO, Path: rel, Detail: "discard", Err: err}
		}
	}
	for d := range dirs {
		_ = data.Remove(d) // only succeeds when empty
	}
	return first
}

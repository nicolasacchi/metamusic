package subsonic

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// argsIgnored are the parameters that never enter args_sha256: credentials and protocol framing (C108.7).
var argsIgnored = map[string]bool{"u": true, "p": true, "t": true, "s": true, "apiKey": true, "c": true, "v": true, "f": true}

// argsSHA256 is the sha256 of the canonical sorted parameters without credentials and framing: one
// "name=value\n" line per value, names sorted, values in request order.
func argsSHA256(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if !argsIgnored[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range params[k] {
			b.WriteString(k + "=" + v + "\n")
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// auditWrite inserts the audit row of a write endpoint after its service call (C108.7). The row carries the
// device principal, never a credential, and the ids created through audit.NoteCreated.
func (s *server) auditWrite(c *call, e *apiError) {
	if s.o.Audit == nil {
		return
	}
	code := ""
	if e != nil {
		code = "SUBSONIC_" + strconv.Itoa(e.code)
	}
	audit.InsertRetrying(c.ctx, s.o.Audit, audit.Entry{
		At: s.now(), Principal: c.principal, Transport: reqinfo.Subsonic, Client: "subsonic:" + c.principal.Client,
		Op: "subsonic:" + c.name, Method: c.r.Method, ArgsSHA256: argsSHA256(c.params), Status: 200, ErrorCode: code,
		CreatedIDs: audit.Created(c.ctx),
	}, 0, 0)
}

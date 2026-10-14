// Package lineage is the Operator Ledger domain: recipe render keys, operators, patch application,
// deterministic seeds and ids, and lineage-tree reads (M3 C71). The pure key functions land in M3-B1;
// lineage never imports library, spend or jobs (TestImportGraph_M3).
package lineage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
)

// Keys returns the two recipe keys of a canonical engine request (C71.7, D-M3-11):
//
//   - contentHash = sha256 of the canonical request without "id" and "seed" (what the song is);
//   - renderHash  = sha256 of the canonical request without "id" (what the engine renders: YuE2's output
//     does not depend on the request id).
//
// The request is the Parsed.Canonical bytes of M1's ParseRequest. Input that is not one JSON object yields
// two empty strings; callers treat that as a programming error.
func Keys(canonical []byte) (contentHash, renderHash string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &obj); err != nil || obj == nil {
		return "", ""
	}
	delete(obj, "id")
	renderHash = hashObject(obj)
	delete(obj, "seed")
	contentHash = hashObject(obj)
	if renderHash == "" || contentHash == "" {
		return "", ""
	}
	return contentHash, renderHash
}

func hashObject(obj map[string]json.RawMessage) string {
	raw, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	c, err := canonjson.Canonical(raw)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:])
}

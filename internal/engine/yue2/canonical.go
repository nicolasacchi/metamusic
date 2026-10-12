package yue2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

func jsonStr(s string) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s) // strings always encode
	return bytes.TrimRight(b.Bytes(), "\n")
}

// CanonicalJSON renders r with keys sorted lexicographically (abc, cfg_scale, cot, id, lyrics, seed,
// style), compact separators, no HTML escaping and no trailing newline. Deterministic.
func CanonicalJSON(r Request) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	if r.ABC != nil {
		b.WriteString(`"abc":`)
		b.Write(jsonStr(*r.ABC))
		b.WriteByte(',')
	}
	if r.CFGScale != nil {
		b.WriteString(`"cfg_scale":`)
		b.WriteString(strconv.FormatFloat(*r.CFGScale, 'g', -1, 64))
		b.WriteByte(',')
	}
	b.WriteString(`"cot":`)
	b.Write(jsonStr(r.Cot))
	b.WriteString(`,"id":`)
	b.Write(jsonStr(r.ID))
	b.WriteString(`,"lyrics":`)
	b.Write(jsonStr(r.Lyrics))
	b.WriteString(`,"seed":`)
	b.WriteString(strconv.FormatInt(r.Seed, 10))
	b.WriteString(`,"style":`)
	b.Write(jsonStr(r.Style))
	b.WriteByte('}')
	return b.Bytes()
}

func hashHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

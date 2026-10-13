// Package canonjson produces deterministic JSON for hashing.
package canonjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

const maxDepth = 64

// Canonical re-encodes raw JSON deterministically: object keys sorted by byte
// order, arrays in order, numbers copied as their exact tokens, strings
// re-encoded without HTML escaping, no whitespace. It errors on invalid JSON,
// invalid UTF-8, duplicate object keys, trailing data and nesting deeper than 64.
func Canonical(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("canonjson: invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out bytes.Buffer
	if err := value(dec, &out, 0); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("canonjson: trailing data")
	}
	return out.Bytes(), nil
}

func value(dec *json.Decoder, out *bytes.Buffer, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("canonjson: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		if depth+1 > maxDepth {
			return errors.New("canonjson: nesting too deep")
		}
		if t == '[' {
			out.WriteByte('[')
			first := true
			for dec.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				if err := value(dec, out, depth+1); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return fmt.Errorf("canonjson: %w", err)
			}
			out.WriteByte(']')
			return nil
		}
		members := map[string][]byte{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("canonjson: %w", err)
			}
			k, ok := kt.(string)
			if !ok {
				return errors.New("canonjson: object key is not a string")
			}
			if _, dup := members[k]; dup {
				return errors.New("canonjson: duplicate object key")
			}
			var vb bytes.Buffer
			if err := value(dec, &vb, depth+1); err != nil {
				return err
			}
			members[k] = vb.Bytes()
		}
		if _, err := dec.Token(); err != nil {
			return fmt.Errorf("canonjson: %w", err)
		}
		keys := make([]string, 0, len(members))
		for k := range members {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, k)
			out.WriteByte(':')
			out.Write(members[k])
		}
		out.WriteByte('}')
	case string:
		writeString(out, t)
	case json.Number:
		out.WriteString(t.String())
	case bool:
		fmt.Fprintf(out, "%t", t)
	case nil:
		out.WriteString("null")
	}
	return nil
}

func writeString(out *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

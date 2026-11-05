package transform

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
)

// Canonical is a spec in its two hashed forms (C202.1). There are two hashes, defined once:
//
//   - SpecHash is the sha256 of the canonical object WITHOUT its id. It is what transforms.spec_hash stores, what reuse
//     (D-T1-2) and the estimate compare, and the source of the id.
//   - RecipeHash is the sha256 of the exact request_json bytes, which INCLUDE the id (M1's rule): recipes.hash and core_hash.
//
// Neither is derived from the other at read time; SpecHash is never recipes.hash.
type Canonical struct {
	ID         string // "dsp-<kind>-<first 20 hex of SpecHash>"
	SpecHash   string // 64 hex
	RecipeHash string // 64 hex
	Bytes      []byte // canonical spec with the id: recipes.request_json
}

// SpecID is the id inside a spec: "dsp-" + kind + "-" + the first 20 hex digits of the spec hash.
func SpecID(kind Kind, specHash string) string {
	if len(specHash) < 20 {
		return ""
	}
	return "dsp-" + string(kind) + "-" + specHash[:20]
}

// Canonicalize computes both hashes and the id of s and returns the canonical bytes with the id. The id field of s is ignored
// and recomputed. Key order and number formatting are canonjson's.
func Canonicalize(s Spec) (Canonical, error) {
	if !s.Kind.Valid() {
		return Canonical{}, fmt.Errorf("transform: canonicalize: unknown kind %q", s.Kind)
	}
	if len(s.Params) == 0 {
		return Canonical{}, fmt.Errorf("transform: canonicalize: a spec has parameters (every default written out)")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return Canonical{}, fmt.Errorf("transform: canonicalize: %w", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return Canonical{}, fmt.Errorf("transform: canonicalize: %w", err)
	}
	delete(members, "id")
	noID, err := json.Marshal(members)
	if err != nil {
		return Canonical{}, fmt.Errorf("transform: canonicalize: %w", err)
	}
	canonNoID, err := canonjson.Canonical(noID)
	if err != nil {
		return Canonical{}, fmt.Errorf("transform: canonicalize: %w", err)
	}
	c := Canonical{SpecHash: sha256Hex(canonNoID)}
	c.ID = SpecID(s.Kind, c.SpecHash)
	idJSON, _ := json.Marshal(c.ID) // a string always marshals
	members["id"] = idJSON
	withID, err := json.Marshal(members)
	if err != nil {
		return Canonical{}, fmt.Errorf("transform: canonicalize: %w", err)
	}
	if c.Bytes, err = canonjson.Canonical(withID); err != nil {
		return Canonical{}, fmt.Errorf("transform: canonicalize: %w", err)
	}
	c.RecipeHash = sha256Hex(c.Bytes)
	return c, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// seedLimit is 2^53: seeds travel through JSON numbers.
const seedLimit = int64(1) << 53

// DefaultSeed is the deterministic default seed of the kinds that use one (C212.2): the first 8 bytes of sha256(trackID),
// big endian, modulo 2^53. Two takes of one glacier differ only when a seed is asked for.
func DefaultSeed(trackID string) int64 {
	h := sha256.Sum256([]byte(trackID))
	return int64(binary.BigEndian.Uint64(h[:8]) % uint64(seedLimit)) //nolint:gosec // modulo 2^53 fits int64
}

// seedKinds are the kinds whose operators draw from the PCG: the others carry seed 0 whatever the request says.
func usesSeed(k Kind) bool {
	switch k {
	case KindShepard, KindGlacier, KindEnsemble, KindBinaural:
		return true
	}
	return false
}

// round rounds v to the given number of decimals; the integer rule (C218.3) is that every value that selects something is on
// a fixed step, so two requests that mean the same thing hash the same.
func round(v float64, decimals int) float64 {
	p := math.Pow10(decimals)
	r := math.Round(v*p) / p
	if r == 0 {
		return 0 // no negative zero in a hashed spec
	}
	return r
}

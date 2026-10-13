package api

import (
	"bytes"
	"encoding/json"
)

// EncodeResponse renders v exactly as Huma writes a response body under
// HumaConfig: its JSON format (an encoder with HTML escaping off, newline
// terminated) with no body transformers (HumaConfig clears CreateHooks, so no
// $schema link is injected). Spend operations store this byte string as the
// idempotent replay body before commit (M3 C80, D-M3-21), so a replay is
// byte-identical to the first answer. TestEncodeResponse_MatchesHumaBody pins
// the equality against a real registered operation.
func EncodeResponse(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

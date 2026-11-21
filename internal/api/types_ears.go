package api

import "github.com/nicolasacchi/metamusic/internal/ears"

// Types of the M5b Ears operations (contracts C330-C349). Domain shapes live in internal/ears with their schemas; this file
// holds the envelopes.

type tierBOut struct{ Body ears.TierB }

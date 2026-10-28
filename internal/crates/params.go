package crates

// Limits of Crates (C118, C119, C125.1). They change only by a decision.
const (
	// MaxManualItems is the number of live members of one manual Crate.
	MaxManualItems = 5000
	// MaxEditBatch is the number of members one edit_crate_items call may add and the number it may remove.
	MaxEditBatch = 200
	// MaxFilterBytes is the largest stored smart filter.
	MaxFilterBytes = 4096
	// MaxName and MaxDescription bound the text columns of crates.
	MaxName        = 80
	MaxDescription = 500
)

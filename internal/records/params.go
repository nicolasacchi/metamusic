package records

import "time"

// Limits and timings of the Records package (C118, C119). They change only by a decision.
const (
	// MaxSlots is the number of live slots one Record may hold.
	MaxSlots = 60
	// MaxEditOps is the number of operations one edit_record_slots call may carry.
	MaxEditOps = 50
	// MaxProposalNote is the longest proposal rationale, in characters.
	MaxProposalNote = 500
	// MaxChangesPage is the largest page of the changelog.
	MaxChangesPage = 200
	// SyncBatch is the number of slots one Syncer pass processes.
	SyncBatch = 200
	// SyncPoll is how often the Syncer looks for unconsumed Pick events while some exist.
	SyncPoll = time.Second
	// CoverParts is the number of slot Songs that feed a Record's cover.
	CoverParts = 12
	// SyncRetries is the number of retries of a write that met SQLITE_BUSY (20 ms x 2^n between them).
	SyncRetries = 5

	// MaxTitle, MaxSubtitle and MaxLinerNote are the length bounds of the text columns of records (C119).
	MaxTitle     = 120
	MaxSubtitle  = 200
	MaxLinerNote = 4000
)

// SyncerActor, SyncerActorKind and SyncerActorName are the actor columns of every row the Syncer writes (C122.2).
const (
	SyncerActor     = "system:records-sync"
	SyncerActorKind = "system"
	SyncerActorName = "records-sync"
)

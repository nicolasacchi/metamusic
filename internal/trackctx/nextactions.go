package trackctx

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// NextActionReader supplies the Muse-aware next-action strings of one take (L3 C139.4). The strings are already
// scope-filtered, rendered and ordered by priority, and each has priority at least nextactions.ContextMuseMinPriority.
// trackctx imports no L3 package; app adapts *nextactions.Service. The reader applies the priority floor and returns at most limit strings; the assembly below owns the room arithmetic (lane NA1).
type NextActionReader interface {
	ForTake(ctx context.Context, p auth.Principal, trackID string, limit int) ([]string, error)
}

// UseNextActions sets the reader; nil (the default) leaves /context byte-identical to the M3 behaviour.
func (b *Builder) UseNextActions(r NextActionReader) { b.nextact = r }

// contextMuseMax mirrors nextactions.ContextMuseMax (trackctx may not import it).
const contextMuseMax = 2

// museAwareActions appends up to min(contextMuseMax, maxNextActions - len(na) - 1) strings after the base rules; the -1
// reserves the critique line. A missing reader, no room, or a reader that fails (including one not implemented yet) leave na unchanged.
func (b *Builder) museAwareActions(ctx context.Context, p auth.Principal, trackID string, na []string) []string {
	if b.nextact == nil {
		return na
	}
	room := min(contextMuseMax, maxNextActions-len(na)-1)
	if room <= 0 {
		return na
	}
	extra, err := b.nextact.ForTake(ctx, p, trackID, room)
	if err != nil {
		return na // next actions are advice: a failing or not yet implemented reader never fails /context
	}
	if len(extra) > room {
		extra = extra[:room]
	}
	return append(na, extra...)
}

// Package nightshift is the night shift (M6N C371-C375): one fixed server-side agent principal that, inside a live mandate, plans a
// small batch of new takes from the Owner's own ratings. This file set is the planner only: it is pure, deterministic given its
// input and a seed, and never spends, creates jobs or batches, or logs lyrics.
package nightshift

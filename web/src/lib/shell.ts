// What a caller's shell shows, decided from whoami.allowed_ops and never from the role name (U1 C313.2, C313.3).
// A viewer without `allowed` (unit tests, a caller that never loaded) keeps every control: hiding is cosmetic and the
// server refuses the click on its own.
import type { Viewer } from "./genIntent";

/** May the viewer invoke this op? True when the viewer carries no allowed list. */
export function mayOp(v: Viewer | null | undefined, op: string): boolean {
  return !v?.allowed || v.allowed.includes(op);
}

/** The Track page panels a Listener or Editor cannot use, each tied to the op its controls call. */
export const TRACK_PANEL_OPS = {
  comments: "list_comments",
  voice: "create_voice_note",
  playlist: "update_playlist",
  context: "get_track_context",
  tags: "set_tags",
  crates: "edit_crate_items",
  records: "edit_record_slots",
  transforms: "create_transform",
} as const;

export type TrackPanel = keyof typeof TRACK_PANEL_OPS;

export function showsPanel(v: Viewer | null | undefined, p: TrackPanel): boolean {
  return mayOp(v, TRACK_PANEL_OPS[p]);
}

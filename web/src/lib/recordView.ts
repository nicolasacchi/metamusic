// View-model helpers for the Records pages (C131.6). Pure: no network, no DOM.
import type { components } from "../api/schema";

type S = components["schemas"];
export type RecordView = S["RecordView"];
export type SlotView = S["SlotView"];
export type SlotOp = S["SlotOp"];
export type RecordStatus = S["Record"]["status"];

/** The slice of whoami the Records pages need. */
export interface Viewer {
  kind: "human" | "agent" | "device";
  scopes: readonly string[];
}

/**
 * May this viewer edit the Record (slots, text, cover, status)? A published Record is edited by a human principal
 * (human or paired device, like the server's ActsAsHuman) only; everything needs the write scope.
 */
export function canEdit(viewer: Viewer | null, record: { status: RecordStatus }): boolean {
  if (!viewer || !viewer.scopes.includes("write")) return false;
  if (record.status === "published") return viewer.kind === "human" || viewer.kind === "device";
  return true;
}

/** Publishing (and the secure page) is for a human principal only. */
export function canPublish(viewer: Viewer | null): boolean {
  return viewer !== null && viewer.kind === "human" && viewer.scopes.includes("write");
}

/** A drag result: move the slot at `from` to index `to`. One `reorder` op with the complete id list, or null when nothing changes. */
export function reorderOp(ids: readonly string[], from: number, to: number): SlotOp | null {
  const n = ids.length;
  if (!Number.isInteger(from) || !Number.isInteger(to) || from < 0 || to < 0 || from >= n || to >= n || from === to) return null;
  const next = [...ids];
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved!);
  return { op: "reorder", slot_ids: next };
}

/** Keyboard / button move by one place: the same op a drag would produce. */
export function moveOp(ids: readonly string[], index: number, delta: -1 | 1): SlotOp | null {
  return reorderOp(ids, index, index + delta);
}

export interface PinControl {
  pin: boolean;
  unpin: boolean;
  label: string;
}

/** Which of Pin / Unpin the slot offers. A slot with no Take offers neither; a pinned slot is always unpinnable. */
export function pinControl(s: Pick<SlotView, "mode" | "state">): PinControl {
  if (s.mode === "pinned") {
    if (s.state === "pin_ineligible") return { pin: false, unpin: true, label: "Pinned, not eligible" };
    return { pin: s.state === "ok", unpin: true, label: "Pinned" };
  }
  if (s.state === "ok") return { pin: true, unpin: false, label: "Floating" };
  return { pin: false, unpin: false, label: "Floating" };
}

const WARNINGS: Record<string, string> = {
  pin_ineligible: "Pinned take is not eligible (discarded); the slot is skipped",
  undecided_pick: "Pick undecided: the take was chosen without a duel win",
  empty: "This slot has no playable take",
  retired_song: "This song was merged into another",
};

export function warningText(code: string): string {
  return WARNINGS[code] ?? code;
}

export function slotWarnings(s: Pick<SlotView, "warnings">): string[] {
  return s.warnings.map(warningText);
}

export interface WarningRow {
  slotId: string | null;
  code: string;
  text: string;
}

/** The Record-level warning list, each row naming its slot position when it has one. */
export function recordWarningRows(v: Pick<RecordView, "warnings" | "slots">): WarningRow[] {
  const pos = new Map(v.slots.map((s) => [s.id, s.position]));
  return v.warnings.map((w) => {
    const p = w.slot_id ? pos.get(w.slot_id) : undefined;
    return { slotId: w.slot_id, code: w.code, text: (p !== undefined ? `Slot ${p}: ` : "") + warningText(w.code) };
  });
}

export interface SlotModel {
  slot: SlotView;
  index: number;
  canUp: boolean;
  canDown: boolean;
  pin: PinControl;
  pinDiffers: boolean;
  undecided: boolean;
  warnings: string[];
}

export function slotModels(v: Pick<RecordView, "slots">): SlotModel[] {
  const last = v.slots.length - 1;
  return v.slots.map((slot, index) => ({
    slot,
    index,
    canUp: index > 0,
    canDown: index < last,
    pin: pinControl(slot),
    pinDiffers: slot.pin_differs,
    undecided: slot.warnings.includes("undecided_pick") || (slot.why ?? "").endsWith("undecided"),
    warnings: slotWarnings(slot),
  }));
}

export const STATUS_LABEL: Record<RecordStatus, string> = { draft: "Draft", proposed: "Proposed", published: "Published" };

/** The row shape of the shared change-text golden (the Go `records.Change`). */
export interface GoldenChange {
  kind: string;
  position?: number | null;
  from_take_id?: string | null;
  to_take_id?: string | null;
  reason?: string | null;
  evidence?: { duels_n?: number; stars?: number; tied?: number } | null;
  detail?: { from?: number; side?: string | null } | null;
}

function whyText(source: string | null | undefined, ev: GoldenChange["evidence"]): string {
  switch (source) {
    case "ladder":
      return ev?.duels_n === undefined ? "won blind duels" : ev.duels_n === 1 ? "won 1 blind duel" : `won ${ev.duels_n} blind duels`;
    case "stars": {
      let s = ev?.stars === undefined ? "stars" : `${ev.stars}★`;
      if (ev?.tied !== undefined && ev.tied > 1) s += ` (newest of ${ev.tied} tied)`;
      return s + " · undecided";
    }
    case "newest":
      return "newest take · undecided";
    case "pin":
    case "pinned":
      return "pinned as the song's Pick";
    case "unpin":
      return "pin removed";
    case "pin_ineligible":
      return "pinned take discarded, fell back";
    case "pin_restored":
      return "pinned take restored";
    case "initial":
      return "first take";
    case "emptied":
      return "no playable take";
    case "retired":
      return "follows the merged song";
    case "duplicate":
      return "duplicate";
  }
  return "";
}

/** Mirror of the Go `records.ChangeText` (C122.3), tested against the shared golden. The server's `ChangeView.text` is what the page shows. */
export function changeText(c: GoldenChange, names: ((takeId: string) => string) | null): string {
  const title = (id: string | null | undefined): string => (id && names ? names(id) || "a take" : "a take");
  const slot = c.position != null ? `Slot ${c.position}` : "Slot";
  const why = c.reason ? whyText(c.reason, c.evidence) : "";
  const withWhy = (s: string) => (why ? `${s} (${why})` : s);
  switch (c.kind) {
    case "created":
      return "Record created";
    case "renamed":
      return "Title changed";
    case "retexted":
      return "Notes changed";
    case "cover_rerolled":
      return "Cover re-rolled";
    case "hidden":
      return "Hidden";
    case "unhidden":
      return "Unhidden";
    case "proposed":
      return "Proposed for publishing";
    case "proposal_dismissed":
      return "Sent back to draft";
    case "published":
      return "Published";
    case "unpublished":
      return "Unpublished";
    case "slot_added":
      return c.to_take_id ? `${slot} added: ${title(c.to_take_id)}` : `${slot} added (no take yet)`;
    case "slot_removed":
      return c.from_take_id ? `${slot} removed (${title(c.from_take_id)})` : `${slot} removed`;
    case "slot_moved": {
      const from = c.detail?.from;
      if (from != null && c.position != null) return `Slot moved ${from} → ${c.position}`;
      return c.position != null ? `Slot moved to ${c.position}` : "Slot moved";
    }
    case "slot_side": {
      if (!c.detail || !("side" in c.detail)) return `${slot} side changed`;
      return c.detail.side == null ? `${slot} side cleared` : `${slot} side ${c.detail.side}`;
    }
    case "slot_pinned":
      return `${slot} pinned to ${title(c.to_take_id)}`;
    case "slot_unpinned":
      return c.to_take_id ? `${slot} unpinned: follows the Pick (${title(c.to_take_id)})` : `${slot} unpinned: follows the Pick (no take yet)`;
    case "slot_filled":
      return withWhy(`${slot} filled: ${title(c.to_take_id)}`);
    case "slot_emptied":
      return c.from_take_id ? `${slot} emptied: no playable take (was ${title(c.from_take_id)})` : `${slot} emptied: no playable take`;
    case "slot_song_followed":
      return `${slot} follows the merged song`;
    case "slot_upgraded":
      return withWhy(`${slot} upgraded: ${title(c.from_take_id)} → ${title(c.to_take_id)}`);
  }
  return "Record changed";
}

/** Calls `fn` exactly once, the first time a load has finished (the Library routeReady signal). */
export function createReadyOnce(fn: () => void) {
  let fired = false;
  return {
    loaded(ok: boolean) {
      if (!ok || fired) return;
      fired = true;
      fn();
    },
  };
}

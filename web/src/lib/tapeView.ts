// View-model helpers for the tape panel (C131.2, C131.6). Pure.
import type { components } from "../api/schema";
import { fmt } from "./time";

type Tape = components["schemas"]["Tape"];
type TapeState = Tape["state"];

export const POLL_MS = 1000;
export const POLL_GIVE_UP_MS = 15 * 60_000;

/** Delay until the next get_tape poll, or null: stop on a terminal state or after 15 minutes. */
export function nextPollMs(state: TapeState, elapsedMs: number): number | null {
  if (state !== "queued" && state !== "running") return null;
  return elapsedMs >= POLL_GIVE_UP_MS ? null : POLL_MS;
}

export function progressText(t: Pick<Tape, "state" | "progress" | "error">): string {
  switch (t.state) {
    case "queued":
      return "Queued";
    case "ready":
      return "Ready";
    case "expired":
      return "The file has expired; export again";
    case "failed":
      return t.error?.message ? `Failed: ${t.error.message}` : "Failed";
    case "running": {
      const p = t.progress;
      if (!p) return "Starting";
      if (p.phase === "decode") return `Decoding track ${p.item} of ${p.items}`;
      if (p.phase === "encode") return `Encoding track ${p.item} of ${p.items}`;
      if (p.phase === "rewrite") return "Writing the file";
      return "Verifying the file";
    }
  }
}

const STATUS_TEXT: Record<Tape["items"][number]["status"], string> = {
  matched: "matched",
  cut_clamped: "cut limited",
  boost_clamped: "boost limited",
  peak_limited: "peak limited",
  silent: "silent",
  unmeasured: "unmeasured",
};

export interface PlanRow {
  position: number;
  slotId: string;
  title: string;
  gain: string;
  predicted: string;
  status: string;
  flags: string[];
}

export function gainText(db: number): string {
  const r = Math.round(db * 10) / 10;
  return `${r > 0 ? "+" : ""}${r.toFixed(1)} dB`;
}

/** The loudness plan, one row per tape item; `titles` maps slot id to the Take title. */
export function planRows(t: Pick<Tape, "items">, titles: ReadonlyMap<string, string>): PlanRow[] {
  return t.items.map((i) => ({
    position: i.position,
    slotId: i.slot_id,
    title: titles.get(i.slot_id) ?? "a take",
    gain: gainText(i.gain_db),
    predicted: i.predicted_lufs === null ? "n/a" : `${i.predicted_lufs.toFixed(1)} LUFS`,
    status: STATUS_TEXT[i.status] ?? i.status,
    flags: [i.lossy ? "lossy" : "", i.resampled ? "resampled" : "", i.upmixed ? "upmixed" : "", i.downmixed ? "downmixed" : ""].filter(Boolean),
  }));
}

/** The lossy-source banner text, or null when every source is lossless. */
export function lossyBanner(t: Pick<Tape, "contains_lossy" | "lossy_sources">): string | null {
  if (!t.contains_lossy && t.lossy_sources === 0) return null;
  const n = t.lossy_sources;
  return `${n} ${n === 1 ? "track comes" : "tracks come"} from a lossy source; the FLAC file is not a lossless copy of them.`;
}

export interface ChapterRow {
  index: number;
  title: string;
  offset: string;
  start_ms: number;
}

export function chapterRows(t: Pick<Tape, "chapters">, titles: ReadonlyMap<string, string>): ChapterRow[] {
  return (t.chapters ?? []).map((c) => ({ index: c.index, title: titles.get(c.slot_id) ?? "a take", offset: fmt(c.start_ms / 1000), start_ms: c.start_ms }));
}

/** Text for a failed export_record_tape / get_tape call. */
export function tapeErrorText(status: number, code: string, message: string): string {
  if (status === 501 || code === "NOT_IMPLEMENTED") return "Tape export is not available yet.";
  if (code === "TAPE_NEEDS_REVIEW") return "This record has warnings. A human has to review and accept them before it can be exported.";
  return message || "Tape export failed.";
}

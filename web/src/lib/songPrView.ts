// Pure view helpers and load states for the Song PR panel and the patch page (M5-W1, C262). Network only through songPrApi.
import type { createApi } from "../api/client";
import type { components } from "../api/schema";
import type { Failure, Res } from "./crateApi";
import { getPatch, listPatches, PATCH_BOUNDS, type PatchOp, type SongPR } from "./songPrApi";
import { fmt } from "./time";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
type Comment = S["Comment"];

/** What a view shows while it loads: the operations answer 501/503 until their service lands ("not available yet"). */
export type Load<T> = { state: "loading" } | { state: "unavailable" } | { state: "error"; message: string } | { state: "ready"; value: T };

export const UNAVAILABLE_TEXT = "Song PRs are not available yet.";

export function isUnavailable(f: Failure): boolean {
  return f.status === 501 || f.status === 503;
}

export function toLoad<T>(r: Res<T>): Load<T> {
  if (r.ok) return { state: "ready", value: r.value };
  return isUnavailable(r) ? { state: "unavailable" } : { state: "error", message: r.message };
}

/** A thrown fetch (offline, rate limit) is a Failure, never an unhandled rejection. */
export async function guarded<T>(fallback: string, run: () => Promise<Res<T>>): Promise<Res<T>> {
  try {
    return await run();
  } catch (e) {
    const limited = e instanceof Error && e.name === "RateLimited";
    return { ok: false, status: 0, code: limited ? "RATE_LIMITED" : "NETWORK", message: limited ? "Too many requests: try again shortly" : `${fallback}: network error`, stale: false };
  }
}

export async function loadPatchList(api: Api, trackId: string): Promise<Load<S["PatchPage"]>> {
  return toLoad(await guarded("Could not list the patches", () => listPatches(api, { track_id: trackId, limit: 20 })));
}

export async function loadPatch(api: Api, id: string): Promise<Load<SongPR>> {
  return toLoad(await guarded("Could not load the patch", () => getPatch(api, id)));
}

/** Open (or regressed) fix comments a patch may address; hidden ones never. */
export function addressable(comments: readonly Comment[]): Comment[] {
  return comments.filter((c) => c.kind === "fix" && !c.hidden && (c.state === "open" || c.state === "regressed"));
}

/** "1:12 · chorus 1 · ~70%": where the comment sits, from its time and its section. */
export function spotText(c: Pick<Comment, "at_second" | "section">): string {
  const parts: string[] = [];
  if (c.at_second !== null) parts.push(fmt(c.at_second));
  if (c.section) parts.push(`${c.section.label} · ~${Math.round(c.section.confidence * 100)}%`);
  return parts.length ? parts.join(" · ") : "no time";
}

export interface LineDraft {
  section_idx: number;
  line_idx: number;
  before: string;
  text: string;
}
export interface PatchDraft {
  comments: string[];
  rationale: string;
  title: string;
  style: string;
  baseStyle: string;
  lines: LineDraft[];
}

const changedLines = (d: PatchDraft) => d.lines.filter((l) => l.text.trim() !== l.before.trim());
const terms = (s: string) => s.split(",").map((t) => t.trim().toLowerCase()).filter(Boolean);

/** Terms added or removed, whichever side is larger (an advisory number; the server decides). */
export function styleTermDelta(before: string, after: string): number {
  const a = new Set(terms(before));
  const b = new Set(terms(after));
  const added = [...b].filter((t) => !a.has(t)).length;
  const removed = [...a].filter((t) => !b.has(t)).length;
  return Math.max(added, removed);
}

export function buildOps(d: PatchDraft): PatchOp[] {
  const ops: PatchOp[] = changedLines(d).map((l) => ({ op: "replace_line", section_idx: l.section_idx, line_idx: l.line_idx, text: l.text.trim() }));
  if (d.style.trim() !== d.baseStyle.trim()) ops.push({ op: "set_style", style: d.style.trim() });
  return ops;
}

export interface BoundsUsage {
  lines: number;
  maxLines: number;
  sections: number;
  maxSections: number;
  styleTerms: number;
  maxStyleTerms: number;
  /** The bounds the draft already exceeds ("lines", "sections", "style"). */
  over: string[];
}
export function boundsUsage(d: PatchDraft): BoundsUsage {
  const ch = changedLines(d);
  const lines = ch.length;
  const sections = new Set(ch.map((l) => l.section_idx)).size;
  const styleTerms = styleTermDelta(d.baseStyle, d.style);
  const over: string[] = [];
  if (lines > PATCH_BOUNDS.maxLines) over.push("lines");
  if (sections > PATCH_BOUNDS.maxSections) over.push("sections");
  if (styleTerms > PATCH_BOUNDS.maxStyleTerms) over.push("style");
  return { lines, maxLines: PATCH_BOUNDS.maxLines, sections, maxSections: PATCH_BOUNDS.maxSections, styleTerms, maxStyleTerms: PATCH_BOUNDS.maxStyleTerms, over };
}

/** A human message when the draft cannot be sent, else null. Mirrors the server's cheap checks; the server stays authoritative. */
export function draftProblem(d: PatchDraft): string | null {
  if (d.comments.length === 0) return "Pick the fix comment this patch addresses.";
  if (d.comments.length > PATCH_BOUNDS.maxAddressed) return `A patch addresses at most ${PATCH_BOUNDS.maxAddressed} comments.`;
  if (d.rationale.trim() === "") return "Say why: one line on what this patch fixes.";
  if (d.rationale.length > 2000) return "The rationale is too long (2 000 characters at most).";
  for (const l of changedLines(d)) {
    const t = l.text.trim();
    if (t === "") return "A replaced line cannot be blank.";
    if (/[\r\n]/.test(t)) return "Each replaced line must be one line.";
    if (t.length > 200) return "A replaced line is at most 200 characters.";
    if (/\[[^\]]*\]/.test(t)) return "A line cannot carry a [section tag].";
  }
  if (d.style.trim().length > 1000) return "The style is too long (1 000 characters at most).";
  const ops = buildOps(d);
  if (ops.length === 0) return "Change at least one line or the style.";
  if (ops.length > PATCH_BOUNDS.maxOps) return `A patch has at most ${PATCH_BOUNDS.maxOps} operations.`;
  const u = boundsUsage(d);
  if (u.over.includes("lines")) return `At most ${u.maxLines} lines may change (this changes ${u.lines}).`;
  if (u.over.includes("sections")) return `At most ${u.maxSections} sections may change (this touches ${u.sections}).`;
  if (u.over.includes("style")) return `At most ${u.maxStyleTerms} style terms may change (this changes ${u.styleTerms}).`;
  return null;
}

/** Distinct non-cancelled batches: what PatchMaxRenders counts. */
export function renderCount(renders: readonly S["PatchRender"][]): number {
  return new Set(renders.filter((r) => r.state !== "cancelled").map((r) => r.batch_id)).size;
}

export function callbackTally(t: S["PatchCallbackTally"]): string {
  if (t.total === 0) return "no callbacks yet";
  const parts = [`${t.answered} of ${t.total} answered`];
  for (const [n, label] of [[t.fixed, "fixed"], [t.not_yet, "not yet"], [t.worse, "worse"], [t.kept, "kept"], [t.lost, "lost"]] as const) {
    if (n > 0) parts.push(`${n} ${label}`);
  }
  return parts.join(" · ");
}

// Pure view helpers for the Transform panel, its cards and the /transforms page (T1 C214): the estimate line,
// resolved-input chips, warning and refusal wording, state chips, ordering and the shared-pool CPU meter.
import type { components } from "../api/schema";

type S = components["schemas"];
export type TransformView = S["TransformView"];
export type TransformEstimate = S["TransformEstimate"];
export type TransformQuota = S["TransformQuota"];
export type TransformWarning = S["TransformWarning"];
export type TransformState = TransformView["state"];
export type Tone = "idle" | "busy" | "ok" | "bad" | "warn";

export const LIVE: readonly TransformState[] = ["queued", "running"];
export const isLive = (s: TransformState): boolean => s === "queued" || s === "running";
/** Cancel is offered on live rows only, and not again once it was asked for. */
export const canCancel = (t: Pick<TransformView, "state" | "cancel_requested_at">): boolean => isLive(t.state) && t.cancel_requested_at === null;

const STATE_LABEL: Record<TransformState, string> = { queued: "Queued", running: "Running", done: "Done", failed: "Failed", cancelled: "Cancelled" };
export const stateLabel = (s: TransformState): string => STATE_LABEL[s] ?? s;
export const stateTone = (s: TransformState): Tone => (s === "done" ? "ok" : s === "failed" ? "bad" : s === "running" ? "busy" : s === "cancelled" ? "warn" : "idle");

export const KIND_LABEL: Record<string, string> = {
  morph: "Morph",
  shepard: "Shepard",
  glacier: "Glacier",
  polytempo: "Polytempo",
  ensemble: "Ensemble",
  score_render: "Render score",
  master: "Master",
  widen: "Widen",
  binaural: "Binaural",
};
export const KIND_ORDER = ["morph", "shepard", "glacier", "polytempo", "ensemble", "master", "widen", "binaural", "score_render"] as const;
export const kindLabel = (k: string): string => KIND_LABEL[k] ?? k;

/** Kinds in the panel's order; kinds the server does not list are not offered, unknown ones follow. */
export function orderKinds(kinds: readonly string[]): string[] {
  const known = KIND_ORDER.filter((k) => kinds.includes(k));
  return [...known, ...kinds.filter((k) => !(KIND_ORDER as readonly string[]).includes(k))];
}

const PHASE_LABEL: Record<string, string> = {
  queued: "Waiting for a slot",
  decode: "Reading the source",
  decoding: "Reading the source",
  align: "Aligning",
  aligning: "Aligning",
  render: "Rendering",
  rendering: "Rendering",
  safety: "Checking loudness",
  encode: "Encoding",
  encoding: "Encoding",
  ingest: "Adding to the library",
  ingesting: "Adding to the library",
};
export function phaseLabel(phase: string | null): string {
  if (!phase) return "";
  return PHASE_LABEL[phase] ?? phase.replaceAll("_", " ");
}

export const progressPercent = (permille: number): number => Math.max(0, Math.min(100, Math.round(permille / 10)));

// ---- numbers -------------------------------------------------------------------------------------------------

/** "14 s", "2 min 5 s", "1 h 3 min". */
export function fmtDuration(totalS: number): string {
  const s = Math.max(0, Math.round(totalS));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 === 0 ? `${m} min` : `${m} min ${s % 60} s`;
  const h = Math.floor(m / 60);
  return m % 60 === 0 ? `${h} h` : `${h} h ${m % 60} min`;
}

/** CPU time: sub-second as ms, else seconds; "≈" is added by the caller. */
export function fmtCpu(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`;
  return fmtDuration(ms / 1000);
}

export function fmtBytes(n: number): string {
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return `${Math.round(n / (1024 * 1024))} MB`;
}

/** `$0.00 · ≈ 14 s of CPU · 3 min 20 s of audio · 180 MB` (cost is always zero; memory is the render's peak). */
export function estimateLine(e: Pick<TransformEstimate, "cost" | "cpu" | "output" | "memory_mb">): string {
  const usd = e.cost.micro_usd === 0 ? "$0.00" : `$${e.cost.usd}`;
  return `${usd} · ≈ ${fmtCpu(e.cpu.expected_ms)} of CPU · ${fmtDuration(e.output.seconds)} of audio · ${e.memory_mb} MB`;
}

export type EstimateInput = TransformEstimate["inputs"][number];
export interface InputChip {
  label: string;
  /** explicit, plan, declared or measured. */
  source: string;
}
/** The resolved tempo and key of an estimate input, each with the source chip. */
export function inputFacts(i: EstimateInput): { role: string; title: string; chips: InputChip[] } {
  const chips: InputChip[] = [];
  if (i.bpm) {
    const fold = i.bpm.octave_fold === 0 ? "" : i.bpm.octave_fold > 0 ? " (doubled)" : " (halved)";
    chips.push({ label: `${trimNum(i.bpm.value)} BPM${fold}`, source: i.bpm.source });
  }
  if (i.key) chips.push({ label: i.key.camelot ? `${i.key.camelot} · ${pcName(i.key.pc)}${i.key.minor ? "m" : ""}` : `${pcName(i.key.pc)}${i.key.minor ? "m" : ""}`, source: i.key.source });
  return { role: roleLabel(i.role), title: i.title, chips };
}
const PCS = ["C", "C♯", "D", "E♭", "E", "F", "F♯", "G", "A♭", "A", "B♭", "B"];
export const pcName = (pc: number): string => PCS[((pc % 12) + 12) % 12] ?? "?";
const trimNum = (n: number): string => String(Math.round(n * 10) / 10);
export function roleLabel(role: string): string {
  const m: Record<string, string> = { a: "A", b: "B", source: "Source", score_source: "Score" };
  return m[role] ?? role.replaceAll("_", " ");
}

// ---- warnings, errors, refusals ------------------------------------------------------------------------------

/** A warning as one line: the server's message, else its code in words. */
export function warningNote(w: TransformWarning): string {
  return w.message || w.code.replaceAll("_", " ");
}
/** Warning codes that read as a serious caution (the rest are informational). */
const CAUTION = new Set(["downbeat_ambiguous", "downbeat_one_drop_suspected", "beat_phase_weak", "tempo_refine_rejected", "master_input_clipped", "widen_out_of_phase_input", "binaural_mono_incompatible", "heavy_pitch_shift", "large_tempo_change", "ensemble_full_mix"]);
export const warningTone = (w: TransformWarning): "caution" | "info" => (CAUTION.has(w.code) ? "caution" : "info");

const ROW_ERROR: Record<string, string> = {
  INVALID_TRANSFORM: "The settings were refused.",
  TRANSFORM_BUDGET_EXCEEDED: "The render needed more CPU than its budget.",
  TRANSFORM_TIMEOUT: "The render took too long and was stopped.",
  TRANSFORM_MEMORY_EXCEEDED: "The render needed more memory than allowed.",
  DSP_NONFINITE: "The render produced invalid audio and was discarded.",
  DSP_SILENT_OUTPUT: "The result was silent and was discarded.",
  LOUDNESS_UNSAFE: "The result could not be made loudness-safe and was discarded.",
  SYNTH_FAILED: "The synthesizer failed.",
  SOUNDFONT_UNKNOWN: "That soundfont is not configured.",
  INPUT_CHANGED: "The source changed while the transform waited.",
  INTERRUPTED: "The server restarted during the render.",
  CANCELLED_BY_USER: "Cancelled.",
  TRANSFORM_FAILED: "The render failed.",
};
/** Row-level error codes (TransformView.error.code), incl. `INGEST_FAILED:<code>`. */
export function rowErrorText(e: { code: string; message: string } | null): string {
  if (!e) return "";
  if (e.code.startsWith("INGEST_FAILED:DUPLICATE_AUDIO")) return "The result is identical to a take you already have.";
  if (e.code.startsWith("INGEST_FAILED")) return "The result could not be added to the library.";
  return ROW_ERROR[e.code] ?? (e.message || e.code);
}

export interface Refusal {
  status: number;
  code: string;
  message: string;
  details: Record<string, unknown>;
  /** Seconds, from Retry-After on a 429 the client turned into an exception. */
  retryAfterS?: number;
}
const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);
const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** "in 3 h 20 min" until an RFC 3339 instant (never negative; unparsable -> ""). */
export function resetsIn(iso: string, now: number): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  return `in ${fmtDuration(Math.max(0, (t - now) / 1000))}`;
}

/** The sentence for a refusal of estimate/create (4xx, 429, 501, 503). Never shows paths or raw details. */
export function refusalText(r: Refusal, now: number = Date.now()): string {
  const d = r.details;
  switch (r.code) {
    case "NOT_IMPLEMENTED":
      return "Transforms are not available on this server yet.";
    case "UNAVAILABLE":
      return str(d.component) === "transform" ? "Transforms are switched off on this server." : `A required tool is unavailable (${str(d.component) || "unknown"}).`;
    case "TRANSFORM_QUOTA_EXCEEDED": {
      const used = num(d.used_ms), res = num(d.reserved_ms), cap = num(d.cap_ms);
      const when = str(d.resets_at) ? ` It resets ${resetsIn(str(d.resets_at), now)}.` : "";
      const left = used !== null && res !== null && cap !== null ? ` ${fmtCpu(used + res)} of ${fmtCpu(cap)} used (shared by everyone of this kind).` : "";
      return `Today's CPU allowance is used up.${left}${when}`;
    }
    case "TRANSFORM_QUEUE_FULL": {
      const q = num(d.queued), run = num(d.running), max = num(d.max);
      const state = q !== null && run !== null ? ` ${run} running, ${q} queued${max !== null ? ` (limit ${max})` : ""}.` : "";
      const retry = r.retryAfterS !== undefined ? ` Try again in ${fmtDuration(r.retryAfterS)}.` : "";
      return `The transform queue is full.${state}${retry}`;
    }
    case "TRANSFORM_TOO_LARGE":
      return `That is too large for one transform${str(d.limit) ? ` (${str(d.limit).replaceAll("_", " ")})` : ""}. Shorten the source or reduce the factor.`;
    case "TRANSFORM_TAKE_NOT_MOTHER":
      return "That take cannot be used as a source.";
    case "LOSSY_MASTER":
      return "A lossy (MP3) take cannot be transformed.";
    case "ALIGNMENT_UNRESOLVED":
      return `The ${str(d.quantity) === "downbeat" ? "downbeat" : "tempo"} of ${str(d.input) ? `input ${str(d.input)}` : "an input"} could not be worked out. Enter it by hand or choose auto.`;
    case "SOUNDFONT_UNKNOWN":
      return "That soundfont is not configured.";
    case "RATE_LIMITED":
      return `Too many requests. Try again in ${fmtDuration(r.retryAfterS ?? 30)}.`;
    case "FORBIDDEN":
      return "You may not do that.";
    case "NOT_FOUND":
      return "That take or transform no longer exists.";
    case "INVALID_TRANSFORM": {
      const f = str(d.field);
      return r.message || (f ? `Check the ${f.replaceAll("_", " ")} setting.` : "Check the settings.");
    }
    default:
      return r.message || "The request failed.";
  }
}
/** True when the server simply does not have transforms (yet): 501 or 503 transform. */
export const isUnavailable = (r: Pick<Refusal, "status" | "code" | "details">): boolean =>
  r.status === 501 || r.code === "NOT_IMPLEMENTED" || (r.status === 503 && r.code === "UNAVAILABLE" && str(r.details.component) === "transform");

// ---- lists and the CPU meter ---------------------------------------------------------------------------------

/** Live rows first (running before queued), then newest first; a stable copy. */
export function sortTransforms<T extends Pick<TransformView, "state" | "created_at" | "id">>(items: readonly T[]): T[] {
  const rank = (s: TransformState) => (s === "running" ? 0 : s === "queued" ? 1 : 2);
  return [...items].sort((a, b) => rank(a.state) - rank(b.state) || (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : a.id < b.id ? 1 : -1));
}

export interface QuotaMeter {
  usedPct: number;
  reservedPct: number;
  /** "12 s of 10 min used · 40 s reserved". */
  label: string;
  full: boolean;
}
/** The shared-pool meter: percent of the cap settled and reserved (clamped, sums to at most 100). */
export function quotaMeter(q: Pick<TransformQuota, "cap_ms" | "used_ms" | "reserved_ms">): QuotaMeter {
  const cap = Math.max(1, q.cap_ms);
  const usedPct = Math.min(100, Math.max(0, Math.round((q.used_ms / cap) * 100)));
  const reservedPct = Math.min(100 - usedPct, Math.max(0, Math.round((q.reserved_ms / cap) * 100)));
  const res = q.reserved_ms > 0 ? ` · ${fmtCpu(q.reserved_ms)} reserved` : "";
  return { usedPct, reservedPct, label: `${fmtCpu(q.used_ms)} of ${fmtCpu(q.cap_ms)} used${res}`, full: q.used_ms + q.reserved_ms >= q.cap_ms };
}

/** Poll delay for the /transforms page: 3 s while anything is live, else none. */
export const pollDelayMs = (items: readonly Pick<TransformView, "state">[]): number | null => (items.some((t) => isLive(t.state)) ? 3000 : null);

export function cpuUseLine(t: Pick<TransformView, "cpu" | "state">): string {
  if (t.cpu.used_ms !== null) return `${fmtCpu(t.cpu.used_ms)} of CPU`;
  return `≈ ${fmtCpu(t.cpu.expected_ms)} of CPU expected`;
}

/**
 * The generic client turns every 429 into an exception that keeps only Retry-After, so the details of
 * TRANSFORM_QUOTA_EXCEEDED / TRANSFORM_QUEUE_FULL are lost. The latest estimate carries the same quota and queue
 * state: when the pool or queue is exhausted, rebuild the refusal from it so the page can say when it resets.
 */
export function explainRateLimit(r: Refusal, e: Pick<TransformEstimate, "quota" | "queue"> | null, queueMax: number | null): Refusal {
  if (r.code !== "RATE_LIMITED" || e === null) return r;
  const q = e.quota;
  if (q.used_ms + q.reserved_ms >= q.cap_ms) {
    return { ...r, code: "TRANSFORM_QUOTA_EXCEEDED", details: { used_ms: q.used_ms, reserved_ms: q.reserved_ms, cap_ms: q.cap_ms, resets_at: q.resets_at } };
  }
  if (queueMax !== null && e.queue.running + e.queue.queued >= queueMax) {
    return { ...r, code: "TRANSFORM_QUEUE_FULL", details: { running: e.queue.running, queued: e.queue.queued, max: queueMax } };
  }
  return r;
}

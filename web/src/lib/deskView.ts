// Pure model of the Desk pages (DESK-W1, m5b-desk C363.2): grouping and filtering of the plan list, the words of a plan, the
// text of a bar, the edit operations of the bar editor and the sentences of its refusals. No network and no Svelte.
import type { DeskFailure, Plan, PlanBarSpan, PlanQuery, PlanSummary, ScoreEditOp, ScoreReport } from "./deskApi";

export type Source = PlanSummary["source"];

export interface DeskFilters {
  source: "" | Source;
  verdict: "" | "liked" | "rejected" | "none";
  rendered: "" | "yes" | "no";
  jobId: string;
  batchId: string;
  baseTrackId: string;
}
export const noFilters: DeskFilters = { source: "", verdict: "", rendered: "", jobId: "", batchId: "", baseTrackId: "" };

/** Filters from the page's query string (`/desk?job_id=` is how the Batch page links a plan job). */
export function filtersFromQuery(q: Record<string, string>): DeskFilters {
  const pick = <T extends string>(v: string | undefined, ok: readonly T[]): T | "" => (ok as readonly string[]).includes(v ?? "") ? (v as T) : "";
  return {
    source: pick(q.source, ["job", "take", "edit"] as const),
    verdict: pick(q.verdict, ["liked", "rejected", "none"] as const),
    rendered: q.rendered === "true" ? "yes" : q.rendered === "false" ? "no" : "",
    jobId: q.job_id ?? "",
    batchId: q.batch_id ?? "",
    baseTrackId: q.base_track_id ?? "",
  };
}

export function filtersToQuery(f: DeskFilters, cursor?: string): PlanQuery {
  return {
    limit: 100,
    ...(f.source ? { source: f.source } : {}),
    ...(f.verdict ? { verdict: f.verdict } : {}),
    ...(f.rendered ? { rendered: f.rendered === "yes" } : {}),
    ...(f.jobId ? { job_id: f.jobId } : {}),
    ...(f.batchId ? { batch_id: f.batchId } : {}),
    ...(f.baseTrackId ? { base_track_id: f.baseTrackId } : {}),
    ...(cursor ? { cursor } : {}),
  };
}

export interface PlanGroup {
  key: string;
  title: string;
  trackId: string | null;
  plans: PlanSummary[];
}

/** Plans grouped by the take (or draft, or plan job) they were made for; groups newest first, plans in reading order. */
export function groupByBase(items: readonly PlanSummary[]): PlanGroup[] {
  const groups = new Map<string, PlanGroup & { newest: string }>();
  for (const p of items) {
    const key = p.base.track_id ? `track:${p.base.track_id}` : p.base.draft_id ? `draft:${p.base.draft_id}` : `job:${p.job_id ?? p.id}`;
    let g = groups.get(key);
    if (!g) {
      g = { key, title: p.base.title || p.title, trackId: p.base.track_id, plans: [], newest: p.created_at };
      groups.set(key, g);
    }
    g.plans.push(p);
    if (p.created_at > g.newest) g.newest = p.created_at;
  }
  const rank = (s: Source) => (s === "take" ? 0 : s === "job" ? 1 : 2);
  return [...groups.values()]
    .sort((a, b) => (a.newest === b.newest ? 0 : a.newest < b.newest ? 1 : -1))
    .map((g) => ({ key: g.key, title: g.title, trackId: g.trackId, plans: [...g.plans].sort((a, b) => rank(a.source) - rank(b.source) || (a.seed_index ?? 0) - (b.seed_index ?? 0) || (a.created_at < b.created_at ? -1 : 1)) }));
}

/** The short name of a plan in a list. */
export function planLabel(p: Pick<PlanSummary, "source" | "seed_index" | "seed">): string {
  if (p.source === "take") return "Take's own score";
  if (p.source === "job") return p.seed_index === null ? `Seed ${p.seed}` : `Seed ${p.seed_index + 1}`;
  return "Edited";
}

export function verdictText(v: "liked" | "rejected" | null): string {
  return v === "liked" ? "Liked" : v === "rejected" ? "Rejected" : "No verdict";
}

/** "3 bars, 4/4, about 12 s" */
export function planFacts(p: Pick<PlanSummary, "bars" | "meter" | "expected_audio_s" | "truncated">): string {
  const parts = [`${p.bars} bar${p.bars === 1 ? "" : "s"}`, p.meter];
  if (p.expected_audio_s > 0) parts.push(`about ${Math.round(p.expected_audio_s)} s`);
  if (p.truncated) parts.push("cut short");
  return parts.join(", ");
}

/** The slice of the score text (byte offsets) a span covers. */
export function spanText(abc: string, s: Pick<PlanBarSpan, "start" | "end">): string {
  const bytes = new TextEncoder().encode(abc);
  return new TextDecoder().decode(bytes.slice(s.start, s.end));
}

export function findSpan(spans: readonly PlanBarSpan[] | null | undefined, voice: string, bar: number): PlanBarSpan | null {
  return (spans ?? []).find((s) => s.voice === voice && s.bar === bar) ?? null;
}

/** The text of a bar, without its closing bar line, as the editor shows it. */
export function barText(abc: string, spans: readonly PlanBarSpan[] | null | undefined, voice: string, bar: number): string | null {
  const s = findSpan(spans, voice, bar);
  return s ? spanText(abc, s) : null;
}

/** The voice ids of a plan, in score order. */
export function voiceIds(p: Pick<Plan, "score">): string[] {
  return (p.score.voices ?? []).map((v) => v.id);
}

/** The section a bar falls in. */
export function sectionOf(p: Pick<Plan, "sections">, bar: number): { label: string; idx: number } | null {
  for (const s of p.sections ?? []) if (bar >= s.start_bar && bar < s.start_bar + s.bars) return { label: s.label || s.kind, idx: s.idx };
  return null;
}

// ---- the bar editor ----------------------------------------------------------------------------------------------------

export type EditMode = "replace" | "transpose" | "tempo";
export interface EditForm {
  mode: EditMode;
  voice: string;
  /** 1-based in the form (musicians count bars from 1); the server counts from 0. */
  from: string;
  to: string;
  abc: string;
  semitones: string;
  bpm: string;
  rationale: string;
}
export const emptyEdit = (voice = "", bar: number | null = null): EditForm => ({
  mode: "replace",
  voice,
  from: bar === null ? "" : String(bar + 1),
  to: bar === null ? "" : String(bar + 1),
  abc: "",
  semitones: "",
  bpm: "",
  rationale: "",
});

export const EDIT_LIMITS = { bpmMin: 20, bpmMax: 300, semitonesMax: 24, rationaleMax: 1000 } as const;

export interface BuiltEdit {
  ops: ScoreEditOp[] | null;
  rationale: string;
  errors: Record<string, string>;
}

function int(raw: string): number | null {
  const t = raw.trim();
  return /^-?\d+$/.test(t) ? Number(t) : null;
}

/** The edit_plan body of the form; the server stays the judge of the structure (bar lengths, meter), this checks the shape. */
export function buildEdit(f: EditForm, bars: number): BuiltEdit {
  const errors: Record<string, string> = {};
  const rationale = f.rationale.trim();
  if ([...rationale].length > EDIT_LIMITS.rationaleMax) errors.rationale = "At most 1000 characters";
  if (f.mode === "tempo") {
    const bpm = int(f.bpm);
    if (bpm === null || bpm < EDIT_LIMITS.bpmMin || bpm > EDIT_LIMITS.bpmMax) errors.bpm = `Enter a tempo, ${EDIT_LIMITS.bpmMin}–${EDIT_LIMITS.bpmMax} BPM`;
    return Object.keys(errors).length > 0 ? { ops: null, rationale, errors } : { ops: [{ op: "set_tempo", bpm: bpm as number }], rationale, errors };
  }
  const from = int(f.from);
  const to = int(f.to);
  if (f.mode === "replace" && f.voice === "") errors.voice = "Choose a voice";
  if (from === null || from < 1 || from > bars) errors.from = `Enter a bar, 1–${bars}`;
  if (to === null || to < 1 || to > bars) errors.to = `Enter a bar, 1–${bars}`;
  else if (from !== null && to < from) errors.to = "The last bar comes before the first";
  let semitones = 0;
  if (f.mode === "transpose") {
    const s = int(f.semitones);
    if (s === null || s === 0 || Math.abs(s) > EDIT_LIMITS.semitonesMax) errors.semitones = `Enter semitones, not 0, within ±${EDIT_LIMITS.semitonesMax}`;
    else semitones = s;
  } else if (f.abc.trim() === "") errors.abc = "Type the new bars (ABC)";
  if (Object.keys(errors).length > 0) return { ops: null, rationale, errors };
  const range = { bar_from: (from as number) - 1, bar_to: (to as number) - 1 };
  const op: ScoreEditOp =
    f.mode === "replace"
      ? { op: "replace_bars", voice: f.voice, ...range, abc: f.abc.trim() }
      : { op: "transpose_bars", ...(f.voice ? { voice: f.voice } : {}), ...range, semitones };
  return { ops: [op], rationale, errors };
}

const RULE_TEXT: Record<string, string> = {
  bad_abc: "The new bars are not valid ABC.",
  bad_op: "That edit is not one the Desk knows.",
  bad_range: "The bars are outside the score.",
  bad_tempo: "That tempo is not allowed.",
  bad_voice: "That voice is not in the score.",
  no_key: "The score has no key line to transpose against.",
  no_ops: "Nothing to change.",
  overlap: "Two edits touch the same bars.",
  shared_span: "Those bars share their text with another voice, so they cannot be edited on their own.",
  too_many_ops: "Too many edits at once (at most 20).",
  transpose_unverified: "That transposition could not be checked, so it was refused.",
  result_unparsable: "The edited score does not read as ABC.",
  source_unparsable: "The plan's own score does not read as ABC.",
  too_large: "The edited score is too long.",
  invariants: "The edit changes the structure of the score.",
};

/** The sentence for a refusal of edit_plan; violations of the structure are listed one per line by the page. */
export function editRefusalText(f: DeskFailure): string {
  if (f.code === "PLAN_NO_CHANGE") return "That edit leaves the score as it is.";
  if (f.code === "PLAN_EDIT_INVALID") {
    const rule = typeof f.details.rule === "string" ? f.details.rule : "";
    const line = typeof f.details.line === "number" && f.details.line > 0 ? ` (line ${f.details.line})` : "";
    return `${RULE_TEXT[rule] ?? f.message}${line}`;
  }
  if (f.status === 403) return "You cannot edit plans.";
  if (f.status === 404) return "That plan no longer exists.";
  return f.message;
}

export interface Violation {
  code: string;
  voice: string;
  /** 1-based, as the page says it. */
  bar: number;
  msg: string;
}
export function violationsOf(f: DeskFailure): Violation[] {
  const v = f.details.violations;
  if (!Array.isArray(v)) return [];
  return v.flatMap((x: unknown) => {
    if (typeof x !== "object" || x === null) return [];
    const o = x as { code?: unknown; voice?: unknown; bar?: unknown; msg?: unknown };
    return [{ code: String(o.code ?? ""), voice: typeof o.voice === "string" ? o.voice : "", bar: (typeof o.bar === "number" ? o.bar : 0) + 1, msg: String(o.msg ?? "") }];
  });
}

/** Why a take could not be opened in the Desk (422 PLAN_SOURCE_UNSUPPORTED, reason in details). */
export function openRefusalText(f: DeskFailure): string {
  if (f.code === "PLAN_SOURCE_UNSUPPORTED") {
    switch (f.details.reason) {
      case "no_score":
        return "This take has no score to open.";
      case "engine":
        return "Only YuE2 takes carry a score the Desk can edit.";
      case "reference":
        return "A reference take has no score of its own.";
      case "lossy":
        return "A lossy import has no score of its own.";
      case "transform":
        return "A transformed take has no score of its own.";
      case "too_large":
        return "This score is too large for the Desk.";
    }
    return "This take has no score the Desk can open.";
  }
  if (f.status === 403) return "You cannot open takes in the Desk.";
  return f.message;
}

export const deskPlanHref = (id: string) => `/desk/plans/${encodeURIComponent(id)}`;

// ---- the score check on the Desk page ------------------------------------------------------------------------------------

const CHECK_RULE: Record<string, string> = {
  empty_score: "the score has no notes",
  too_large: "the score is too long",
  too_many_voices: "the score has more voices than the Desk can play",
  too_many_events: "the score has too many notes or bars",
};

/** What check_abc found, as lines of words: whether the text reads, its shape, and each syntax error with its place. */
export function reportLines(r: Pick<ScoreReport, "ok" | "bars" | "voices" | "header" | "duration_s" | "errors" | "warnings">): { ok: boolean; lines: string[] } {
  if (!r.ok) {
    const errs = (r.errors ?? []).map((e) => `Line ${e.line}, column ${e.col}: ${CHECK_RULE[e.rule] ?? e.rule.replaceAll("_", " ")}`);
    return { ok: false, lines: errs.length > 0 ? errs : ["The score does not read as ABC."] };
  }
  const v = r.voices ?? [];
  const shape = [`${r.bars} bar${r.bars === 1 ? "" : "s"}`, `${v.length} voice${v.length === 1 ? "" : "s"}`, r.header.meter, r.duration_s !== null ? `about ${Math.round(r.duration_s)} s` : ""].filter(Boolean).join(", ");
  return { ok: true, lines: [`Reads as ABC: ${shape}.`, ...(r.warnings ?? []).map((w) => `Warning on line ${w.line}: ${w.code.replaceAll("_", " ")}`)] };
}

// The server answers `null` for a verdicts entry nobody has set and for the edit of a plan that is not an edit, where
// schema.d.ts says object: these read them defensively.
type Loose<T> = T | null | undefined;
export const ownerVerdictOf = (p: { verdicts?: Loose<{ owner?: Loose<{ verdict: "liked" | "rejected" | null }> }> }): "liked" | "rejected" | null => p.verdicts?.owner?.verdict ?? null;
export const criticVerdictOf = (p: { verdicts?: Loose<{ critic?: Loose<{ verdict: "liked" | "rejected" | null }> }>; critic_verdict?: "liked" | "rejected" | null }): "liked" | "rejected" | null =>
  p.verdicts?.critic?.verdict ?? p.critic_verdict ?? null;
export const editRationaleOf = (p: { edit?: Loose<{ rationale: string | null }> }): string | null => p.edit?.rationale ?? null;

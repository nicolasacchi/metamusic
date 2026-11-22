// Pure model of the /transforms/score page (DESK-T4, m5b-desk C363.4): the form of the score_render transform (pasted ABC or
// a Take that carries a score; soundfont, dialect, chords, BPM, tail, gain), its request body, whether the kind is available
// on this server, and the words for its refusals. No network and no Svelte: the page runs the estimate and the render
// through transformApi.ts, exactly as the Transform panel does.
import type { components } from "../api/schema";
import { refusalText, type Refusal } from "./transformView";

type S = components["schemas"];
export type TransformRequest = S["TransformRequest"];
export type ScoreCaps = Pick<S["TransformCapabilities"], "enabled" | "kinds" | "soundfonts" | "tools">;

/** The server's limits (kind_score.go, limits.go); the server stays the judge, this only saves a round trip. */
export const SCORE_LIMITS = { bpmMin: 20, bpmMax: 300, tailMax: 10, tailDefault: 3, gainMin: 0.1, gainMax: 1.5, gainDefault: 0.6, maxAbcBytes: 262144 } as const;

export type ScoreSource = "abc" | "take";
export interface ScoreForm {
  source: ScoreSource;
  abc: string;
  trackId: string;
  /** "" = the server's first configured soundfont. */
  soundfont: string;
  /** "" = the default for the source (yue2 for a YuE2 take, else standard). */
  dialect: "" | "standard" | "yue2";
  chords: "" | "none" | "block";
  bpm: string;
  tail: string;
  gain: string;
  title: string;
}
export const defaultScoreForm = (trackId = ""): ScoreForm => ({
  source: trackId ? "take" : "abc",
  abc: "",
  trackId,
  soundfont: "",
  dialect: "",
  chords: "",
  bpm: "",
  tail: "",
  gain: "",
  title: "",
});

export type Availability = { available: true; soundfonts: string[] } | { available: false; reason: string };

/**
 * Whether a score render can be asked for. The capabilities list the kind only for a configured soundfont plus a working
 * fluidsynth; the reasons below say which piece is missing so the page can say it before any click.
 */
export function scoreAvailability(caps: ScoreCaps | null): Availability {
  if (caps === null) return { available: false, reason: "Rendering a score is not available right now." };
  if (!caps.enabled) return { available: false, reason: "Transforms are not available on this server yet, so a score cannot be rendered." };
  const fonts = caps.soundfonts.map((f) => f.name);
  if (caps.kinds.some((k) => k.kind === "score_render") && fonts.length > 0 && caps.tools.fluidsynth.available) return { available: true, soundfonts: fonts };
  if (!caps.tools.fluidsynth.available) return { available: false, reason: "Rendering a score is not available on this server yet: the synthesizer (fluidsynth) is not installed." };
  if (fonts.length === 0) return { available: false, reason: "Rendering a score is not available on this server yet: no soundfont is configured." };
  return { available: false, reason: "Rendering a score is not available on this server yet." };
}

export type FieldErrors = Record<string, string>;
export interface BuiltScore {
  body: TransformRequest | null;
  errors: FieldErrors;
}

function optional(raw: string, label: string, lo: number, hi: number, errors: FieldErrors, key: string): number | null {
  const t = raw.trim();
  if (t === "") return null;
  const n = Number(t);
  if (!Number.isFinite(n)) errors[key] = "Enter a number";
  else if (n < lo || n > hi) errors[key] = `${label} must be ${lo}–${hi}`;
  else return n;
  return null;
}

/** The create/estimate body; blank optional fields go out as null (the server's default), a bad one is a per-field error. */
export function buildScoreRequest(f: ScoreForm): BuiltScore {
  const errors: FieldErrors = {};
  let score: { abc: string } | { from_track_id: string } = { abc: "" };
  if (f.source === "abc") {
    if (f.abc.trim() === "") errors.abc = "Paste the score (ABC notation)";
    else if (new TextEncoder().encode(f.abc).length > SCORE_LIMITS.maxAbcBytes) errors.abc = "The score is too long (at most 256 KiB)";
    else score = { abc: f.abc };
  } else if (f.trackId.trim() === "") errors.trackId = "Choose a take";
  else score = { from_track_id: f.trackId.trim() };
  const bpm = optional(f.bpm, "BPM", SCORE_LIMITS.bpmMin, SCORE_LIMITS.bpmMax, errors, "bpm");
  const tail = optional(f.tail, "Tail", 0, SCORE_LIMITS.tailMax, errors, "tail");
  const gain = optional(f.gain, "Gain", SCORE_LIMITS.gainMin, SCORE_LIMITS.gainMax, errors, "gain");
  if (Object.keys(errors).length > 0) return { body: null, errors };
  const title = f.title.trim();
  const body = {
    kind: "score_render",
    title: title === "" ? null : title,
    score_render: { score, dialect: f.dialect || null, soundfont: f.soundfont || null, bpm, tail_s: tail, gain, chords: f.chords || null },
  } as TransformRequest;
  return { body, errors };
}

/** 503 UNAVAILABLE for the score kind's own tools (fluidsynth or the soundfont), as opposed to "transforms are off". */
export const scoreUnavailableRefusal = (r: Pick<Refusal, "status" | "code" | "details">): boolean =>
  r.status === 503 && r.code === "UNAVAILABLE" && (r.details.component === "fluidsynth" || r.details.component === "soundfont");

const RULE_TEXT: Record<string, string> = {
  empty: "The score is empty.",
  no_score: "No tune was found in the score.",
  too_long: "The score is too long.",
};
/** The sentence for a refusal of estimate or create on this page. */
export function scoreRefusalText(r: Refusal): string {
  if (scoreUnavailableRefusal(r)) {
    const what = r.details.component === "fluidsynth" ? "the synthesizer (fluidsynth) is not installed" : "no soundfont is configured";
    return `Rendering a score is not available on this server yet: ${what}.`;
  }
  if (r.code === "SCORE_INVALID") {
    const rule = typeof r.details.rule === "string" ? r.details.rule : "";
    const line = typeof r.details.line === "number" && r.details.line > 0 ? ` (line ${r.details.line}${typeof r.details.col === "number" && r.details.col > 0 ? `, column ${r.details.col}` : ""})` : "";
    return `${RULE_TEXT[rule] ?? `The score could not be read${rule ? ` (${rule.replaceAll("_", " ")})` : ""}.`}${line}`;
  }
  if (r.code === "SOUNDFONT_UNKNOWN") return "That soundfont is not configured. Choose one from the list.";
  return refusalText(r);
}

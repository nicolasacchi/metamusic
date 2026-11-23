// Pure model of the audition player (DESK-W2, m5b-desk C363.3): which bar is sounding at a time, the request body of an
// audition, the words for its refusals and the label of a stored one. The player itself (AuditionPlayer.svelte) decodes the
// signed MP3 on the shared AudioContext; it never makes a second audio element or a second synthesizer.
import type { CreateAuditionBody, DeskFailure, PlanAudition, PlanBarSpan } from "./deskApi";
import type { BarRef } from "./abcRender";

export interface BarStart extends BarRef {
  start_s: number;
}

/** Bar start times of the first voice that has any, in bar order (every voice shares the bar clock). */
export function barStarts(spans: readonly PlanBarSpan[] | null | undefined, voices: readonly string[]): BarStart[] {
  const all = spans ?? [];
  const order = [...voices, ...all.map((s) => s.voice)];
  for (const v of order) {
    const mine = all.filter((s) => s.voice === v && s.start_s !== null).sort((a, b) => a.bar - b.bar);
    if (mine.length > 0) return mine.map((s) => ({ voice: s.voice, bar: s.bar, start_s: s.start_s as number }));
  }
  return [];
}

/** The last bar that has started at time `t` (seconds into the audition), or null before the first one. */
export function barAt(starts: readonly BarStart[], t: number): BarRef | null {
  let hit: BarStart | null = null;
  for (const b of starts) {
    if (b.start_s <= t) hit = b;
    else break;
  }
  return hit ? { voice: hit.voice, bar: hit.bar } : null;
}

export interface AuditionForm {
  /** "" = the server's first configured soundfont. */
  soundfont: string;
  /** "" = the default (block). */
  chords: "" | "none" | "block";
  tail: string;
  gain: string;
}
export const defaultAuditionForm: AuditionForm = { soundfont: "", chords: "", tail: "", gain: "" };

/** The create_plan_audition body: only what the user chose; the server fills the rest (so the same choice is the same cached file). */
export function auditionBody(f: AuditionForm): CreateAuditionBody {
  const body: CreateAuditionBody = {};
  if (f.soundfont) body.soundfont = f.soundfont;
  if (f.chords) body.chords = f.chords;
  const tail = f.tail.trim() === "" ? NaN : Number(f.tail);
  if (Number.isFinite(tail)) body.tail_s = tail;
  const gain = f.gain.trim() === "" ? NaN : Number(f.gain);
  if (Number.isFinite(gain)) body.gain = gain;
  return body;
}

/** "m:ss" */
export function clock(s: number): string {
  const t = Number.isFinite(s) && s > 0 ? Math.floor(s) : 0;
  return `${Math.floor(t / 60)}:${String(t % 60).padStart(2, "0")}`;
}

export const auditionLine = (a: Pick<PlanAudition, "soundfont" | "chords" | "seconds">): string =>
  `${a.soundfont} · ${a.chords === "block" ? "chords" : "notes only"} · ${clock(a.seconds)}`;

/** The sentence for a refused create_plan_audition or get_plan_audition. */
export function auditionRefusalText(f: DeskFailure): string {
  const d = f.details;
  const num = (k: string): number | null => (typeof d[k] === "number" ? (d[k] as number) : null);
  switch (f.code) {
    case "AUDITION_BUSY":
      return `The synthesizer is busy with another render. Try again${num("retry_after_s") ? ` in about ${num("retry_after_s")} s` : " in a moment"}.`;
    case "AUDITION_LIMIT":
      return `That is today's limit of ${num("limit") ?? "new"} auditions${typeof d.reset_at === "string" ? ` (it resets at ${d.reset_at})` : ""}. Auditions you already made can still be played.`;
    case "AUDITION_TOO_LONG":
      return `This score is too long to audition${num("max") ? ` (at most ${num("max")} s)` : ""}.`;
    case "SOUNDFONT_UNKNOWN":
      return "That soundfont is not configured. Choose one from the list.";
    case "SCORE_INVALID": {
      const line = num("line");
      const col = num("col");
      return `The score could not be read${typeof d.rule === "string" && d.rule ? ` (${d.rule.replaceAll("_", " ")})` : ""}${line ? ` at line ${line}${col ? `, column ${col}` : ""}` : ""}.`;
    }
    case "AUDITION_FAILED":
      return "The audition could not be made. Try again, or edit the score.";
    case "UNAVAILABLE":
      return d.component === "fluidsynth" || d.component === "soundfont" || d.component === "transform"
        ? "Auditions are not available on this server yet: the synthesizer or its soundfont is missing."
        : f.message;
    default:
      return f.message;
  }
}

/** After a failed fetch of the audio: an expired signed link is retried once with a fresh one, anything else is final. */
export function playOutcome(e: unknown, attempt: number): "retry" | "fail" {
  const expired = e instanceof Error && (e.name === "ClipLinkExpired" || e.message === "Clip link expired");
  return expired && attempt < 1 ? "retry" : "fail";
}

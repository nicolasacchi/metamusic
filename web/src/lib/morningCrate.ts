// Morning Crate helpers and the crate listening session (M6N-W2, C384). Pure TypeScript: the player, the outbox and the
// clock are injected, so every rule below is unit tested without a browser.
import type { components } from "../api/schema";
import { HeadsetGrammar, mmss, type HeadsetAction, type HeadsetEvent, type HeadsetPos, type HeadsetPreset } from "./headset";

export type MorningCrate = components["schemas"]["MorningCrate"];
export type MorningCrateSummary = components["schemas"]["MorningCrateSummary"];
export type MorningChapter = components["schemas"]["MorningChapter"];
export type MorningSide = components["schemas"]["MorningSide"];
export type MorningEvent = components["schemas"]["MorningEvent"];
export type MorningSettings = components["schemas"]["MorningSettings"];

/** A queued event also remembers the crate it belongs to (the endpoint is per crate). */
export type MorningOutEvent = MorningEvent & { crate_id: string };

/** Queue items of a crate side: `morning:<crate id>:<side>`. Never persisted (the colon fails isTrackId). */
export const MORNING_PREFIX = "morning:";
const ITEM = /^morning:([A-Za-z0-9_-]{1,64}):(\d{1,3})$/;

export const morningItem = (crateId: string, side: number): string => `${MORNING_PREFIX}${crateId}:${side}`;
export const isMorningItem = (id: string): boolean => id.startsWith(MORNING_PREFIX);

export function parseMorningItem(item: string): { crate: string; side: number } | null {
  const m = ITEM.exec(item);
  return m ? { crate: m[1]!, side: Number(m[2]) } : null;
}

const byOrder = (a: MorningChapter, b: MorningChapter) => a.n - b.n;

/** The chapters of one side in play order. */
export function sideChapters(view: Pick<MorningCrate, "chapters">, side: number): MorningChapter[] {
  return view.chapters.filter((c) => c.side === side).sort(byOrder);
}

/** The chapter that plays at `sideMs` of `side` (a slate belongs to the chapter it announces); null when the side has none. */
export function chapterAt(view: Pick<MorningCrate, "chapters">, side: number, sideMs: number): MorningChapter | null {
  const cs = sideChapters(view, side);
  let hit: MorningChapter | null = null;
  for (const c of cs) {
    if (c.start_ms <= sideMs) hit = c;
    else break;
  }
  return hit ?? cs[0] ?? null;
}

export function nextChapter(view: Pick<MorningCrate, "chapters">, n: number): MorningChapter | null {
  return view.chapters.find((c) => c.n === n + 1) ?? null;
}

export function previousChapter(view: Pick<MorningCrate, "chapters">, n: number): MorningChapter | null {
  return view.chapters.find((c) => c.n === n - 1) ?? null;
}

/** Lock-screen and mini-player title of a chapter: blind until the reveal, the take's title after it. */
export function chapterTitle(view: Pick<MorningCrate, "chapters" | "revealed">, c: MorningChapter): string {
  if (view.revealed && c.track) return c.track.title;
  return `Take ${c.n} of ${view.chapters.length}`;
}

/** The player bar's label of a side: "Side A". */
export function sideLabel(view: Pick<MorningCrate, "sides">, side: number): string {
  return view.sides.find((s) => s.side === side)?.label ?? `Side ${String.fromCharCode(65 + side)}`;
}

/** Chapters without a blind rating yet (the badge on New; the anchors count: nothing tells them apart before the reveal). */
export function unratedCount(view: Pick<MorningCrate, "chapters">): number {
  return view.chapters.filter((c) => c.stars === null).length;
}

/** "Today" in the browser's zone, by the crate's creation time. */
export function isToday(iso: string, now: Date): boolean {
  const d = new Date(iso);
  return d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
}

export function crateName(c: Pick<MorningCrateSummary, "created_at">): string {
  const d = new Date(c.created_at);
  return `Morning crate ${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
}

export function minutesText(ms: number): string {
  return `${Math.max(1, Math.round(ms / 60_000))} min`;
}

export const VERDICT_TEXT: Record<MorningChapter["verdict"], string> = {
  kept: "Kept",
  skipped: "Skipped",
  marked: "Marked",
  heard: "Heard",
  unheard: "Not heard",
};

/** A sides' links are usable when the file exists and the link has not run out. */
export function sideUrl(s: MorningSide | undefined, nowMs: number): string | null {
  if (!s || s.removed || !s.url) return null;
  if (s.expires_at && Date.parse(s.expires_at) <= nowMs) return null;
  return s.url;
}

/** A side as the offline manager downloads it. */
export interface SideFile {
  side: number;
  label: string;
  url: string;
  sha256: string;
  bytes: number;
  duration_s: number;
}

export function offlineSides(view: Pick<MorningCrate, "sides">, nowMs: number): SideFile[] {
  const out: SideFile[] = [];
  for (const s of view.sides) {
    const url = sideUrl(s, nowMs);
    if (url && s.sha256 && s.bytes > 0) out.push({ side: s.side, label: s.label, url, sha256: s.sha256, bytes: s.bytes, duration_s: Math.round(s.duration_ms / 1000) });
  }
  return out;
}

/** The two takes of a duel picked from a revealed crate: both must be real takes. */
export function duelPair(view: Pick<MorningCrate, "chapters">, picked: number[]): [string, string] | null {
  if (picked.length !== 2) return null;
  const ids = picked.map((n) => view.chapters.find((c) => c.n === n)?.track?.id ?? "");
  return ids[0] && ids[1] && ids[0] !== ids[1] ? [ids[0], ids[1]] : null;
}

export interface SessionDeps {
  /** Where the audio element is: the queue item playing and the second in it. */
  position(): { trackId: string; sec: number } | null;
  /** Queue an event for the server (the morning outbox). */
  submit(e: MorningOutEvent): void;
  seek(sec: number): void;
  /** Start another side at `ms`. */
  playSide(side: number, ms: number): void;
  /** Nothing follows the last chapter: let the side end. */
  finish(side: number): void;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(h: unknown): void;
  /** The chapter changed: lock-screen metadata. */
  describe?(title: string, album: string): void;
  flash?(text: string): void;
  vibrate?(): void;
  id(): string;
  now(): Date;
  preset?: HeadsetPreset;
  latencyMs?: number;
}

/** One listening session over one crate view: turns headset presses and playback progress into events. */
export class CrateSession {
  readonly crateId: string;
  view: MorningCrate;
  private deps: SessionDeps;
  private grammar: HeadsetGrammar;
  private shown = -1;

  constructor(view: MorningCrate, deps: SessionDeps) {
    this.view = view;
    this.crateId = view.id;
    this.deps = deps;
    this.grammar = new HeadsetGrammar(
      {
        position: () => this.pos(),
        emit: (e) => this.emit(e),
        seekNext: (p) => this.toNext(p),
        setTimer: deps.setTimer,
        clearTimer: deps.clearTimer,
        flash: deps.flash,
        vibrate: deps.vibrate,
        latencyMs: deps.latencyMs,
      },
      deps.preset ?? "prev_marks",
    );
  }

  /** The crate was re-read (a reveal, a fresh link): same session, new view. */
  update(view: MorningCrate): void {
    this.view = view;
    this.shown = -1;
  }

  /** The listener's place in this crate, or null when the player is elsewhere. */
  pos(): HeadsetPos | null {
    const p = this.deps.position();
    const it = p ? parseMorningItem(p.trackId) : null;
    if (!p || !it || it.crate !== this.crateId) return null;
    const sideMs = Math.max(0, Math.round(p.sec * 1000));
    const c = chapterAt(this.view, it.side, sideMs);
    return c ? { side: it.side, sideMs, chapter: c.n } : null;
  }

  press(a: HeadsetAction): void {
    this.grammar.press(a);
  }
  setPreset(p: HeadsetPreset): void {
    this.grammar.preset = p;
  }
  mark(): void {
    this.grammar.mark();
  }
  skip(): void {
    this.grammar.skip();
  }
  flush(): void {
    this.grammar.flush();
  }
  /** A seek the listener made (or the session itself): nothing is completed for what was left. */
  jumped(): void {
    this.grammar.jumped();
  }
  dispose(): void {
    this.grammar.dispose();
  }

  /** Previous: back to the start of this chapter, or of the one before when it has just begun. Never marks. */
  back(): void {
    const p = this.pos();
    if (!p) return;
    const cur = this.view.chapters.find((c) => c.n === p.chapter);
    if (!cur) return;
    this.jumped();
    const target = p.sideMs - cur.start_ms > 3000 ? cur : (previousChapter(this.view, cur.n) ?? cur);
    this.go(target);
  }

  /** Playback progressed (timeupdate). */
  tick(): void {
    const p = this.pos();
    if (!p) return;
    const prev = this.shown;
    if (p.chapter !== this.shown) {
      this.shown = p.chapter;
      const c = this.view.chapters.find((x) => x.n === p.chapter);
      if (c) this.deps.describe?.(chapterTitle(this.view, c), sideLabel(this.view, c.side));
    }
    const left = prev > 0 && prev !== p.chapter ? this.view.chapters.find((x) => x.n === prev) : undefined;
    this.grammar.observe(p, left ? { side: left.side, sideMs: left.end_ms, chapter: left.n } : undefined);
  }

  /** The element's `ended` for a side of this crate. */
  ended(trackId: string): void {
    const it = parseMorningItem(trackId);
    if (!it || it.crate !== this.crateId) return;
    const last = sideChapters(this.view, it.side).at(-1);
    if (last) this.grammar.ended({ side: it.side, sideMs: last.end_ms, chapter: last.n });
    this.grammar.flush();
  }

  private toNext(p: HeadsetPos): void {
    this.jumped();
    const next = nextChapter(this.view, p.chapter);
    if (next) this.go(next);
    else this.deps.finish(p.side);
  }

  private go(c: MorningChapter): void {
    const here = this.deps.position();
    const it = here ? parseMorningItem(here.trackId) : null;
    if (it && it.side === c.side) this.deps.seek(c.start_ms / 1000);
    else this.deps.playSide(c.side, c.start_ms);
  }

  private emit(e: HeadsetEvent): void {
    const out: MorningOutEvent = {
      crate_id: this.crateId,
      id: this.deps.id(),
      side: e.side,
      chapter: e.chapter,
      kind: e.kind,
      side_ms: e.side_ms,
      client_at: this.deps.now().toISOString(),
      ...(e.latency_ms !== undefined ? { latency_ms: e.latency_ms } : {}),
    };
    this.deps.submit(out);
  }
}

/** A blind rating of one chapter (1..5, or null to clear), positioned at the chapter's start. */
export function rateEvent(view: Pick<MorningCrate, "id" | "chapters">, chapter: number, stars: number | null, id: string, now: Date): MorningOutEvent | null {
  const c = view.chapters.find((x) => x.n === chapter);
  if (!c || (stars !== null && (!Number.isInteger(stars) || stars < 1 || stars > 5))) return null;
  return { crate_id: view.id, id, side: c.side, chapter: c.n, kind: "rate", side_ms: c.start_ms, client_at: now.toISOString(), stars };
}

/** The settings form: every figure as text, so a bad entry is shown instead of silently coerced. */
export interface SettingsForm {
  enabled: boolean;
  at: string;
  minutes: string;
  side_minutes: string;
  max_takes: string;
  anchors: string;
  include_truncated: boolean;
  keep_days: string;
  wait_minutes: string;
}

/** The server's ranges (MORNING_SETTINGS_INVALID); the form checks them first so the message names the field. */
export const SETTING_RANGES = {
  minutes: { label: "Crate length (minutes)", lo: 5, hi: 120 },
  side_minutes: { label: "Longest side (minutes)", lo: 5, hi: 15 },
  max_takes: { label: "Most takes", lo: 1, hi: 30 },
  anchors: { label: "Calibration anchors", lo: 0, hi: 2 },
  keep_days: { label: "Keep sides (days)", lo: 1, hi: 30 },
  wait_minutes: { label: "Wait for the night batch (minutes)", lo: 0, hi: 120 },
} as const;

export function settingsToForm(s: MorningSettings): SettingsForm {
  return {
    enabled: s.enabled,
    at: s.at,
    minutes: String(s.minutes),
    side_minutes: String(s.side_minutes),
    max_takes: String(s.max_takes),
    anchors: String(s.anchors),
    include_truncated: s.include_truncated,
    keep_days: String(s.keep_days),
    wait_minutes: String(s.wait_minutes),
  };
}

export type PatchResult = { ok: true; body: components["schemas"]["MorningSettingsPatch"] } | { ok: false; error: string };

/** Only what changed goes into the patch; an empty patch is refused. */
export function formToPatch(f: SettingsForm, cur: MorningSettings): PatchResult {
  const body: components["schemas"]["MorningSettingsPatch"] = {};
  const at = f.at.trim();
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(at)) return { ok: false, error: "The time must be HH:MM in 24-hour form, for example 07:00." };
  if (at !== cur.at) body.at = at;
  if (f.enabled !== cur.enabled) body.enabled = f.enabled;
  if (f.include_truncated !== cur.include_truncated) body.include_truncated = f.include_truncated;
  for (const k of Object.keys(SETTING_RANGES) as (keyof typeof SETTING_RANGES)[]) {
    const r = SETTING_RANGES[k];
    const text = f[k].trim();
    const n = Number(text);
    if (text === "" || !Number.isInteger(n) || n < r.lo || n > r.hi) return { ok: false, error: `${r.label}: a whole number from ${r.lo} to ${r.hi}.` };
    if (n !== cur[k]) body[k] = n;
  }
  if (Object.keys(body).length === 0) return { ok: false, error: "Nothing changed." };
  return { ok: true, body };
}

/** One line for a crate in the list. */
export function crateLine(c: MorningCrateSummary): string {
  if (c.state === "building") return "Building…";
  if (c.state === "empty") return "Nothing to put in it that day.";
  if (c.state === "failed") return `Failed${c.error_code ? ` (${c.error_code})` : ""}.`;
  return `${c.takes} takes, ${minutesText(c.duration_ms)}, ${c.sides} ${c.sides === 1 ? "side" : "sides"}${c.revealed ? ", revealed" : ", blind"}.`;
}

export { mmss };

const PRESET_KEY = "mm:headset:preset";

/** The headset preset this device uses (previous marks by default); storage may be missing or full. */
export function loadPreset(storage: Pick<Storage, "getItem"> | undefined = typeof localStorage === "undefined" ? undefined : localStorage): HeadsetPreset {
  try {
    return storage?.getItem(PRESET_KEY) === "next_marks" ? "next_marks" : "prev_marks";
  } catch {
    return "prev_marks";
  }
}

export function savePreset(p: HeadsetPreset, storage: Pick<Storage, "setItem"> | undefined = typeof localStorage === "undefined" ? undefined : localStorage): void {
  try {
    storage?.setItem(PRESET_KEY, p);
  } catch {
    /* the preset then lasts for this page only */
  }
}

/** The server answered a batch of this crate: reload the page only when it revealed a crate the page still shows blind. */
export function shouldReloadAfterAnswer(answer: { crate_id: string; revealed?: boolean }, id: string, local: Pick<MorningCrate, "revealed"> | null | undefined): boolean {
  return answer.crate_id === id && answer.revealed === true && local?.revealed === false;
}

/** Before the reveal call: a marker still inside its window is committed, then every queued event is sent and settled. */
export async function settleBeforeReveal(session: { flush(): void } | null | undefined, outbox: { flush(): Promise<void>; idle(): Promise<void> }): Promise<void> {
  session?.flush();
  await outbox.flush();
  await outbox.idle();
}

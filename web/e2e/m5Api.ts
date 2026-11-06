// API helpers for the M5 specs (Song PR, callbacks, heat, section duels, voice, receipts). Like the L3 and T1 helpers, every
// spec makes its own entities through the operations the SPA uses and identifies them by id, never by a total another spec
// could change. Nothing here deletes.
import { randomBytes, randomUUID } from "node:crypto";
import type { APIRequestContext, Page } from "@playwright/test";
import { apiJSON, uniq } from "./helpers";
import { allTerminal, assertOnlyDuplicateFailures, composeRoots, createHuman, estimate, first, pollBatch, type Batch } from "./m3Api";

export interface SectionOut {
  idx: number;
  kind: string;
  label: string;
  lines: string[];
  start_s: number | null;
  end_s: number | null;
  source: string;
}
export interface TrackDetail {
  id: string;
  title: string;
  duration_s: number;
  sections: SectionOut[];
  recipe: { style: string };
}
export interface CommentOut {
  id: string;
  track_id: string;
  body: string;
  kind: "note" | "fix" | "keep" | "marker";
  state: "open" | "addressed" | "resolved" | "regressed";
  at_second: number | null;
  origin_callback_id?: string;
  voice?: { url: string; duration_s: number } | null;
}
export interface PatchOut {
  id: string;
  title: string | null;
  operator: string;
  renders: { batch_id: string; job_id: string; state: string; seed: number; child_track_id: string | null }[];
  callbacks: { total: number; answered: number; fixed: number; not_yet: number; worse: number; kept: number; lost: number };
  addresses: { comment_id: string; state: string }[];
}
export interface CallbackOut {
  id: string;
  kind: "fix" | "keep";
  state: "pending" | "answered";
  patch_id: string;
  child: { id: string; title: string; seed: number };
  comment: { id: string; track_id: string; state: string; body: string; kind: string };
  spot: { child_at_s: number; source_at_s: number | null; basis: string; label: string | null };
  my_verdict: { verdict: string } | null;
}
export interface SubmitOut {
  replayed: boolean;
  verdict: { verdict: string; state_after: string | null };
  comment_state: string | null;
  regression_comment_id: string | null;
}

export const trackDetail = (request: APIRequestContext, id: string) => apiJSON<TrackDetail>(request, "GET", `/api/tracks/${id}`);

export const comments = async (request: APIRequestContext, trackId: string): Promise<CommentOut[]> =>
  (await apiJSON<{ items: CommentOut[] }>(request, "GET", `/api/tracks/${trackId}/comments?limit=100`)).items;

export const commentById = async (request: APIRequestContext, trackId: string, id: string): Promise<CommentOut> => {
  const c = (await comments(request, trackId)).find((x) => x.id === id);
  if (!c) throw new Error(`comment ${id} missing on ${trackId}`);
  return c;
};

export const addComment = (request: APIRequestContext, trackId: string, kind: "fix" | "keep" | "note", body: string, at?: number) =>
  apiJSON<CommentOut>(request, "POST", `/api/tracks/${trackId}/comments`, { body, kind, ...(at === undefined ? {} : { at_second: at }) }, true);

/** A moment inside the take, rounded to 0.1 s: `part` of the way in. */
export const spotAt = (durationS: number, part: number): number => Math.max(0.1, Math.floor(durationS * part * 10) / 10);

/** One new composed root (fake backend, known recipe, lossless, lyrics Verse x4 lines / Chorus x4 lines). */
export async function composeBase(request: APIRequestContext, label: string): Promise<TrackDetail> {
  return trackDetail(request, first(await composeRoots(request, 1, label)));
}

export const proposePatch = (request: APIRequestContext, baseTrackId: string, commentIds: string[], rationale: string, ops: unknown[], title?: string) =>
  apiJSON<PatchOut>(request, "POST", "/api/patches", { base_track_id: baseTrackId, addresses_comment_ids: commentIds, rationale, ops, ...(title ? { title } : {}) }, true);

export const getPatch = (request: APIRequestContext, id: string) => apiJSON<PatchOut>(request, "GET", `/api/patches/${id}`);

/** A replace_line op for the base's first section, line `line`. */
export const replaceLine = (sectionIdx: number, lineIdx: number, text: string) => ({ op: "replace_line", section_idx: sectionIdx, line_idx: lineIdx, text });

/** Prices a Song PR render and confirms it as the e2e human (human_create: never needs an approval); returns the settled batch. */
export async function renderPatch(request: APIRequestContext, patchId: string, nSeeds = 1): Promise<Batch> {
  const e = await estimate(request, "/api/generations/estimate", { items: [{ mode: "song_pr", patch_id: patchId, n_seeds: nSeeds }] });
  const made = await createHuman(request, e, "e2e song pr");
  const b = await pollBatch(request, made.id, allTerminal, 120_000);
  assertOnlyDuplicateFailures(b);
  return b;
}

/** The children (track ids) of the completed jobs of a batch. */
export const childrenOf = (b: Batch): string[] => b.jobs.filter((j) => j.state === "completed" && j.track_id).map((j) => j.track_id ?? "");

export interface ClosedLoop {
  base: TrackDetail;
  fixA: CommentOut;
  fixB: CommentOut;
  keep: CommentOut;
  patch: PatchOut;
  children: string[];
}

/**
 * The whole front half of the loop through the API: a composed base with two fix comments and a keep comment, one patch
 * addressing both fixes, rendered until two children exist (a take can end DUPLICATE_AUDIO in the fake renderer, see
 * m3Api.DUPLICATE_AUDIO; a render that lost a take is repeated, at most three renders in all). Each child gets callbacks.
 */
export async function closeTheLoop(request: APIRequestContext, label: string): Promise<ClosedLoop> {
  const base = await composeBase(request, label);
  const at = spotAt(base.duration_s, 0.4);
  const fixA = await addComment(request, base.id, "fix", uniq("fix-a the second line drags"), at);
  const fixB = await addComment(request, base.id, "fix", uniq("fix-b the chorus is thin"), spotAt(base.duration_s, 0.7));
  const keep = await addComment(request, base.id, "keep", uniq("keep the opening"), spotAt(base.duration_s, 0.1));
  const patch = await proposePatch(request, base.id, [fixA.id, fixB.id], "e2e: tighten the first verse line", [replaceLine(0, 1, "la la la la")]);
  const children: string[] = [];
  for (let round = 0; round < 3 && children.length < 2; round++) children.push(...childrenOf(await renderPatch(request, patch.id, 2)));
  if (children.length < 2) throw new Error(`the Song PR rendered ${children.length} children in three renders`);
  return { base, fixA, fixB, keep, patch: await getPatch(request, patch.id), children };
}

export const callbacksOf = async (request: APIRequestContext, query: string): Promise<CallbackOut[]> =>
  (await apiJSON<{ items: CallbackOut[] }>(request, "GET", `/api/callbacks?limit=100&${query}`)).items;

export const pendingTotal = async (request: APIRequestContext): Promise<number> =>
  (await apiJSON<{ total: number }>(request, "GET", "/api/callbacks?state=pending&limit=1")).total;

/** Logs the listen events of one session as the e2e human: a play to `playedMs`, a rewind to `backToMs`, and the replay to the same point. */
export async function logRewindSession(request: APIRequestContext, trackId: string, playedMs: number, backToMs: number): Promise<void> {
  const session = `m5-${randomBytes(6).toString("hex")}`;
  const at = new Date().toISOString();
  const ev = (kind: string, from?: number, to?: number) => ({
    id: randomUUID(),
    track_id: trackId,
    session_id: session,
    kind,
    client_at: at,
    ...(from === undefined ? {} : { from_ms: from, to_ms: to }),
  });
  const body = { events: [ev("start"), ev("segment", 0, playedMs), ev("seek", playedMs, backToMs), ev("segment", backToMs, playedMs), ev("complete")] };
  const out = await apiJSON<{ accepted: number; rejected: unknown[] }>(request, "POST", "/api/listen-events", body);
  if (out.accepted !== body.events.length || out.rejected.length > 0) throw new Error(`listen events: ${JSON.stringify(out)}`);
}

export interface Heat {
  bins: number;
  sessions: number;
  rewinds: number[] | null;
  hotspots: { from_s: number; to_s: number; label: string | null }[] | null;
}
export const heatOf = (request: APIRequestContext, trackId: string) => apiJSON<Heat>(request, "GET", `/api/tracks/${trackId}/heat`);

/**
 * What a clean run must not show: console errors, uncaught page errors and any 501/503 answer of an M5 operation (the "not
 * available yet" states the panels show for them). `allow` names console texts a test provokes on purpose (a stubbed 503).
 */
export function watchM5(page: Page, allow: RegExp[] = []): { problems: () => string[] } {
  const found: string[] = [];
  page.on("pageerror", (e) => found.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    if (allow.some((re) => re.test(m.text()))) return;
    found.push(`console: ${m.text()}`);
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    const m5 = /^\/api\/(patches|callbacks|duels\/suggestions)|\/heat$|\/voice-notes$/.test(u.pathname);
    if (m5 && (r.status() === 501 || r.status() === 503) && !allow.some((re) => re.test(`${r.status()} ${u.pathname}`))) found.push(`${r.status()} ${r.request().method()} ${u.pathname}`);
  });
  return { problems: () => [...found] };
}

/** Horizontal overflow of the page: the document scrolls sideways, or an element sticks out past the right edge of the viewport. */
export async function overflowX(page: Page): Promise<{ scroll: number; client: number; offenders: string[] }> {
  return page.evaluate(() => {
    const client = document.documentElement.clientWidth;
    const offenders: string[] = [];
    for (const el of document.body.querySelectorAll<HTMLElement>("*")) {
      const cs = getComputedStyle(el);
      if (cs.display === "none" || cs.visibility === "hidden" || cs.position === "fixed") continue;
      if (el.closest("[inert], [hidden], dialog:not([open]), svg, canvas, audio")) continue;
      const r = el.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) continue;
      if (r.right > client + 1) offenders.push(`${el.tagName.toLowerCase()}[${el.getAttribute("data-testid") ?? el.className}] right=${Math.round(r.right)}`);
      if (offenders.length >= 8) break;
    }
    return { scroll: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth), client, offenders };
  });
}

// API helpers for the M3 specs. Every M3 spec makes its own mothers (a composed root through the same human
// operations the SPA uses) so that no spec depends on the children, jobs or counters another spec creates.
import type { APIRequestContext, APIResponse } from "@playwright/test";
import { apiJSON, idemKey, uniq } from "./helpers";

export interface Estimate {
  confirmation_token: string;
  takes: number;
  requires_human_approval: boolean;
  items: { takes: { seed: number; title: string }[] }[];
}
export interface JobSummary {
  id: string;
  state: string;
  title: string;
  request_id: string;
  seed: number;
  parent_track_id: string | null;
  draft_id: string | null;
  track_id: string | null;
  error_code: string | null;
}
export interface Batch {
  id: string;
  state: string;
  jobs: JobSummary[];
  counts: Record<string, number>;
  cost: { outstanding_micro_usd: number; settled_micro_usd: number };
  approval: { state: string; reasons: string[]; via: string | null };
}

const TERMINAL = ["completed", "failed", "cancelled", "expired"];

/** A style that the fake backend renders as a slow job ("fake:slow=<s>", C78.2). */
export const slowStyle = (seconds: number) => `english, synth pop, 100 bpm fake:slow=${seconds}`;

export function recipe(id: string, seed: number, style = "english, synth pop, 100 bpm") {
  return { id, style, lyrics: "[Verse]\nla la\nla\nla\nla\n[Chorus]\nla\nla\nla\nla", cot: "full", seed };
}

export function estimate(request: APIRequestContext, path: string, body: unknown): Promise<Estimate> {
  return apiJSON<Estimate>(request, "POST", path, body);
}

/** Confirms an estimate as the e2e human (human_create: never needs an approval). */
export function createHuman(request: APIRequestContext, e: Estimate, rationale = "e2e"): Promise<Batch> {
  return apiJSON<Batch>(request, "POST", "/human/api/generations", { confirmation_token: e.confirmation_token, rationale }, true);
}

export function getBatch(request: APIRequestContext, id: string): Promise<Batch> {
  return apiJSON<Batch>(request, "GET", `/api/batches/${id}`);
}

/** Polls until `done` holds; the fake backend is fast, so the generous timeout only covers a busy CI host. */
export async function pollBatch(request: APIRequestContext, id: string, done: (b: Batch) => boolean, timeoutMs = 90_000): Promise<Batch> {
  const deadline = Date.now() + timeoutMs;
  let last: Batch | undefined;
  while (Date.now() < deadline) {
    last = await getBatch(request, id);
    if (done(last)) return last;
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`batch ${id} did not reach the wanted state in ${timeoutMs} ms (state ${last?.state}, counts ${JSON.stringify(last?.counts)})`);
}

export const allTerminal = (b: Batch) => b.jobs.length > 0 && b.jobs.every((j) => TERMINAL.includes(j.state));

/**
 * The fake worker draws an attempt's tone from 1000 variants, so a take can collide with an existing track's audio and
 * end DUPLICATE_AUDIO (D-M3-32): a property of the simulated renderer, not of the product. Specs that need a take to
 * complete therefore run again with a new attempt when, and only when, that is the one reason a take failed.
 */
export const DUPLICATE_AUDIO = "DUPLICATE_AUDIO";
const MAX_ROUNDS = 8;

/** True when every job is terminal and each one that did not complete ended DUPLICATE_AUDIO. */
export function onlyDuplicates(b: Batch): boolean {
  return allTerminal(b) && b.jobs.filter((j) => j.state !== "completed").every((j) => j.error_code === DUPLICATE_AUDIO);
}

/** Fails the test when a terminal batch holds any failure that is not the simulated renderer's audio collision. */
export function assertOnlyDuplicateFailures(b: Batch): void {
  const bad = b.jobs.filter((j) => j.state !== "completed" && j.error_code !== DUPLICATE_AUDIO);
  if (bad.length > 0) throw new Error(`jobs ended ${JSON.stringify(bad.map((j) => [j.state, j.error_code]))}`);
}

/** Composes `n` new root tracks (one human confirmation each) and returns their track ids. */
export async function composeRoots(request: APIRequestContext, n: number, label: string): Promise<string[]> {
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    for (let round = 0; ; round++) {
      const tag = uniq(label);
      const e = await estimate(request, "/api/generations/estimate", { items: [{ mode: "compose_new", title: tag, recipe: recipe(tag, 100_000 + Math.floor(Math.random() * 1_000_000)) }] });
      const b = await pollBatch(request, (await createHuman(request, e, "e2e mother")).id, allTerminal);
      assertOnlyDuplicateFailures(b);
      const j = first(b.jobs);
      if (j.state === "completed" && j.track_id) {
        out.push(j.track_id);
        break;
      }
      if (round >= MAX_ROUNDS) throw new Error("composing an e2e mother collided with existing audio too often");
    }
  }
  return out;
}

/** `n` completed new_seed children of `mother` (confirmed by the human); a colliding take is made again. */
export async function newSeedChildren(request: APIRequestContext, mother: string, n: number): Promise<JobSummary[]> {
  const done: JobSummary[] = [];
  for (let round = 0; done.length < n; round++) {
    if (round > MAX_ROUNDS) throw new Error("the e2e children collided with existing audio too often");
    const e = await estimate(request, `/api/tracks/${mother}/variants/estimate`, { mode: "new_seed", n_seeds: n - done.length });
    const b = await pollBatch(request, (await createHuman(request, e, "e2e children")).id, allTerminal);
    assertOnlyDuplicateFailures(b);
    done.push(...b.jobs.filter((j) => j.state === "completed"));
  }
  return done;
}

/** A started (not awaited) slow style-edit batch of `n` takes: it keeps its workers busy until it is terminated. */
export async function startSlowBatch(request: APIRequestContext, mother: string, n: number): Promise<Batch> {
  const e = await estimate(request, `/api/tracks/${mother}/variants/estimate`, { mode: "style_edit", n_seeds: n, patch: { style: slowStyle(60) } });
  return createHuman(request, e, "e2e slow");
}

export async function expectOK(r: APIResponse, what: string): Promise<void> {
  if (!r.ok()) throw new Error(`${what}: ${r.status()} ${await r.text()}`);
}

/** The agent's Idempotency-Key header for a direct create. */
export const agentCreateHeaders = () => ({ "Idempotency-Key": idemKey() });

/** The only element of a list that must have one (the strict index checks make `a[0]` possibly undefined). */
export function first<T>(a: readonly T[]): T {
  const x = a[0];
  if (x === undefined) throw new Error("expected a non-empty list");
  return x;
}

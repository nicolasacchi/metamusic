// API helpers for the E2 specs (the second engine, ACE-Step). Every spec makes its own takes through the operations the SPA
// uses and identifies them by id, never by a total another spec could change. The engine is adopted through the operator CLI
// of the spec's own server (`metamusic engines adopt`): that is the one human path to adoption, there is no REST or MCP one.
import { spawnSync } from "node:child_process";
import type { APIRequestContext, Page } from "@playwright/test";
import type { WorkerServer } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { allTerminal, assertOnlyDuplicateFailures, createHuman, estimate, first, pollBatch, type Batch, type Estimate, type JobSummary } from "./m3Api";

export const ACE = "acestep15";

export interface EngineRow {
  name: string;
  label: string;
  enabled: boolean;
  selectable: boolean;
  unavailable_reason: string | null;
  adopt_hint: string | null;
  adoption: { state: "adopted" | "not_adopted" | "none"; via: string | null };
  backend?: { name: string; enabled: boolean; armed: boolean; tripped: boolean };
  caps: Record<string, unknown>;
  features?: { unverified: string[]; tasks: string[] };
}

export async function listEngines(request: APIRequestContext): Promise<EngineRow[]> {
  return (await apiJSON<{ engines: EngineRow[] }>(request, "GET", "/api/engines")).engines;
}

export async function aceRow(request: APIRequestContext): Promise<EngineRow> {
  const row = (await listEngines(request)).find((e) => e.name === ACE);
  if (!row) throw new Error("list_engines does not list acestep15");
  return row;
}

/** The operator's adoption of ACE-Step on this server (the CLI over the server's own data dir; never an agent path). */
export function adoptEngine(server: WorkerServer): void {
  const bin = process.env.MM_E2E_BIN_RESOLVED;
  if (!bin) throw new Error("MM_E2E_BIN_RESOLVED is set by global-setup");
  const r = spawnSync(bin, ["engines", "adopt", ACE, "--reason", "e2e: adopt the engine under test"], { env: server.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`engines adopt failed (exit ${r.status ?? r.signal}): ${r.stderr}`);
}

/** The operator's rejection (a new row of the append-only log): the not-adopted state a retried test starts from. */
export function rejectEngine(server: WorkerServer): void {
  const bin = process.env.MM_E2E_BIN_RESOLVED;
  if (!bin) throw new Error("MM_E2E_BIN_RESOLVED is set by global-setup");
  const r = spawnSync(bin, ["engines", "reject", ACE, "--reason", "e2e: back to not adopted"], { env: server.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`engines reject failed (exit ${r.status ?? r.signal}): ${r.stderr}`);
}

/** Adopts when not adopted yet (a retried test finds it done). */
export async function ensureAdopted(server: WorkerServer, request: APIRequestContext): Promise<void> {
  if ((await aceRow(request)).adoption.state !== "adopted") adoptEngine(server);
  const row = await aceRow(request);
  if (!row.selectable) throw new Error(`acestep15 is not selectable after adoption: ${row.unavailable_reason}`);
}

/** A compose_new ACE-Step estimate body: the request rides in the item's recipe (the frozen ItemIn.recipe). */
export function composeBody(id: string, secs: number, seed: number, extra: Record<string, unknown> = {}) {
  const rec = {
    id,
    task: "text2music",
    caption: "synth pop, warm",
    lyrics: "[verse]\nla la la\n[chorus]\nso so so",
    instrumental: false,
    vocal_language: "en",
    duration_ds: secs * 10,
    seed,
    thinking: true,
    inference_steps: 8,
    ...extra,
  };
  return { engine: ACE, items: [{ mode: "compose_new", engine: ACE, recipe: rec, title: `e2 ${id}` }] };
}

export interface Created {
  est: Estimate;
  batch: Batch;
  jobs: JobSummary[];
}

/** Estimates `body`, confirms it as the e2e human and waits for every job to end. */
export async function runBody(request: APIRequestContext, body: unknown): Promise<Created> {
  const est = await estimate(request, "/api/generations/estimate", body);
  const created = await createHuman(request, est, "e2e second engine");
  const batch = await pollBatch(request, created.id, allTerminal, 150_000);
  return { est, batch, jobs: batch.jobs };
}

const MAX_ROUNDS = 6;

/**
 * Runs the bodies `make(round)` returns until every job completed: the fake renderer draws a tone from a small set, so a take
 * can collide with existing audio (DUPLICATE_AUDIO), a property of the simulation that a new seed removes.
 */
export async function runCompleted(request: APIRequestContext, make: (round: number) => unknown): Promise<Created> {
  for (let round = 0; ; round++) {
    const c = await runBody(request, make(round));
    assertOnlyDuplicateFailures(c.batch);
    if (c.jobs.every((j) => j.state === "completed")) return c;
    if (round >= MAX_ROUNDS) throw new Error("an ACE-Step take collided with existing audio too often");
  }
}

/** An ACE-Step text2music root take of `secs` seconds (the source of the edit specs); returns its track id. */
export async function aceBase(request: APIRequestContext, secs = 30): Promise<string> {
  const c = await runCompleted(request, (round) => composeBody(uniq("e2base"), secs, 7000 + round * 13 + Math.floor(Math.random() * 100_000)));
  const id = first(c.jobs).track_id;
  if (!id) throw new Error("the ACE-Step base job has no track");
  return id;
}

/** A port of a YuE2 take (the cross-engine child). */
export async function acePort(request: APIRequestContext, yue2Parent: string): Promise<string> {
  const c = await runCompleted(request, () => ({ engine: ACE, items: [{ mode: "port", parent_track_id: yue2Parent }] }));
  const id = first(c.jobs).track_id;
  if (!id) throw new Error("the port job has no track");
  return id;
}

/** An ACE-Step reference render following [from, to] of `track` (the default window when omitted). */
export async function aceReference(request: APIRequestContext, track: string, from?: number, to?: number): Promise<string> {
  const ref = { track_id: track, ...(from !== undefined && to !== undefined ? { from_s: from, to_s: to } : {}) };
  const c = await runCompleted(request, () => ({ engine: ACE, items: [{ mode: "reference", parent_track_id: track, reference: ref }] }));
  const id = first(c.jobs).track_id;
  if (!id) throw new Error("the reference job has no track");
  return id;
}

/** Puts a generation intent where /human/generate reads it (what the Track page does before it navigates). */
export async function setIntent(page: Page, intent: Record<string, unknown>): Promise<void> {
  await page.goto("/");
  await page.evaluate((i) => sessionStorage.setItem("mm.genIntent", JSON.stringify(i)), intent);
}

/**
 * What a clean E2 run must not show: console errors, uncaught page errors, any 4xx/5xx answer of list_engines and (with
 * `forbidEngines`) any request to it at all. Call `.problems()` at the end of a test.
 */
export function watchE2(page: Page, forbidEngines = false): { problems: () => string[]; engineCalls: () => number } {
  const found: string[] = [];
  let calls = 0;
  page.on("pageerror", (e) => found.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    // A failed fetch is logged with the resource's URL as the location; only list_engines counts here (other lanes' stubs
    // that still answer 501 are not this lane's to judge).
    const where = m.location().url;
    if (m.text().startsWith("Failed to load resource") && where && !new URL(where).pathname.startsWith("/api/engines")) return;
    found.push(`console: ${m.text()}`);
  });
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/api/engines") {
      calls++;
      if (forbidEngines) found.push(`unexpected list_engines call from ${page.url()}`);
    }
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname === "/api/engines" && r.status() >= 400) found.push(`${r.status()} ${r.request().method()} ${u.pathname}`);
  });
  return { problems: () => [...found], engineCalls: () => calls };
}

/** True when the page scrolls sideways (the document is wider than the window). */
export function overflowsX(page: Page): Promise<{ scroll: number; client: number }> {
  return page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }));
}

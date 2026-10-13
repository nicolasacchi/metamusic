// Shared helpers for the M2a specs. Specs run in parallel on one server and one library, so every
// spec makes its own entities unique (tags, comment bodies, playlist names) and projects that run the
// same spec use different seed tracks.
import { randomBytes } from "node:crypto";
import type { APIRequestContext, Locator, Page, TestInfo } from "@playwright/test";

export interface Item {
  id: string;
  title: string;
  duration_s: number;
}

export async function allTracks(request: APIRequestContext): Promise<Item[]> {
  const r = await request.get("/api/tracks?limit=50");
  return ((await r.json()) as { items: Item[] }).items;
}

export async function trackByTitle(request: APIRequestContext, title: string): Promise<Item> {
  const t = (await allTracks(request)).find((x) => x.title === title);
  if (!t) throw new Error(`seed track ${title} missing`);
  return t;
}

/**
 * Two seed tracks that no other project uses for the same spec: the six "Syn song" tracks (the three
 * "Syn ears" tracks belong to the M2b Ears specs and sort before them, so they are left out) are sorted
 * by (title, id) and each project owns a pair. Ratings, statuses and tags set on them are undone by the spec.
 */
export async function ownTracks(request: APIRequestContext, info: TestInfo): Promise<[Item, Item]> {
  const all = (await allTracks(request)).filter((x) => x.title.startsWith("Syn song")).sort((x, y) => (x.title === y.title ? (x.id < y.id ? -1 : 1) : x.title < y.title ? -1 : 1));
  const slot = Math.max(0, ["chrome", "chrome-pixel7", "chrome-iphone15"].indexOf(info.project.name));
  const a = all[slot * 2];
  const b = all[slot * 2 + 1];
  if (!a || !b) throw new Error("the e2e profile has six Syn song tracks");
  return [a, b];
}

export function uniq(prefix: string): string {
  return `${prefix}-${randomBytes(3).toString("hex")}`;
}

/** Tag names are lowercase [a-z0-9-]; this stays inside that. */
export function uniqTag(prefix = "jt"): string {
  return uniq(prefix).toLowerCase();
}

export const audioTime = (page: Page) => page.evaluate(() => document.querySelector("audio")?.currentTime ?? -1);

let n = 0;
/** A unique Idempotency-Key for direct API calls from a spec. */
export function idemKey(): string {
  n += 1;
  return `e2e-${randomBytes(6).toString("hex")}-${n}`;
}

export async function apiJSON<T>(request: APIRequestContext, method: "GET" | "POST" | "PATCH" | "PUT", path: string, body?: unknown, create = false): Promise<T> {
  const headers = create ? { "Idempotency-Key": idemKey() } : undefined;
  const r = await request.fetch(path, { method, ...(body === undefined ? {} : { data: body }), ...(headers ? { headers } : {}) });
  if (!r.ok()) throw new Error(`${method} ${path} -> ${r.status()} ${await r.text()}`);
  return (await r.json()) as T;
}

/**
 * Click a control in the sticky player bar by its box. Playwright's own click scrolls the document to
 * bring a sticky element "into view" and then fails the hit test against whatever sits there on phones;
 * a real tap does not scroll.
 */
export async function tap(page: Page, target: Locator): Promise<void> {
  await target.waitFor({ state: "visible" });
  const b = await target.boundingBox();
  if (!b) throw new Error("tap target has no box");
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

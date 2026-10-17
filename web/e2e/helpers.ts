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

const TAB_LABELS = ["New", "Library", "Play queue", "Judge", "More"];
/** Sub-views of the Library and Judge tabs; "Takes" and "One by one" are the tabs' own landing pages. */
const SUB_VIEWS: Record<string, string> = { Sounds: "Library", Playlists: "Library", Duels: "Judge", Ladder: "Judge" };
const LANDINGS: Record<string, string> = { Takes: "Library", "One by one": "Judge" };

/**
 * Navigate by the app shell like a user: tap the tab, then (if the destination is a sub-view or a More row) the link on
 * the page it opens. The same two taps on a phone (tab bar) and on desktop (top nav). Secure (/human/*) rows do a
 * full-page load.
 */
export async function openNav(page: Page, label: string): Promise<void> {
  const nav = page.getByRole("navigation", { name: "Main" });
  const tab = (name: string) => nav.getByRole("link", { name: new RegExp(`^${name}\\b`) });
  const top = TAB_LABELS.includes(label) ? label : (LANDINGS[label] ?? SUB_VIEWS[label] ?? "More");
  await tab(top).click();
  if (top === label || LANDINGS[label] === top) return;
  const dest = SUB_VIEWS[label] ? page.getByRole("navigation", { name: `${top} views` }) : page.getByRole("main");
  await dest.getByRole("link", { name: new RegExp(`^${label}\\b`) }).click();
}

/**
 * Client-side navigation without touching the shell (history + popstate, what the router listens to). For specs that
 * mock or count requests the More page would also make (/api/me, /api/ping).
 */
export async function spaGoto(page: Page, path: string): Promise<void> {
  await page.evaluate((p) => {
    history.pushState(null, "", p);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, path);
}

/**
 * Library filters: on a phone (touch) they live in a "Filters (n)" sheet. Opens it if it is there and closed;
 * a no-op on desktop, where the form is inline. The sheet is a modal dialog, so close it (closeFilters) before tapping the list.
 */
export async function openFilters(page: Page): Promise<void> {
  const open = page.getByTestId("filters-open");
  if ((await open.count()) === 0) return;
  if (await page.getByTestId("filters-sheet").evaluate((d) => (d as HTMLDialogElement).open)) return;
  await open.click();
  await page.getByTestId("filters-sheet").waitFor({ state: "visible" });
}

export async function closeFilters(page: Page): Promise<void> {
  const sheet = page.getByTestId("filters-sheet");
  if ((await sheet.count()) === 0 || !(await sheet.evaluate((d) => (d as HTMLDialogElement).open))) return;
  await sheet.getByRole("button", { name: "Done" }).click();
}

interface Small {
  what: string;
  w: number;
  h: number;
}

/**
 * Every visible interactive element must be at least 44x44 CSS px. A checkbox or radio counts by its label,
 * inline prose links (an anchor inside running text) are exempt, and elements clipped away or off-screen
 * (screen-reader-only) are not targets.
 */
export async function smallTargets(page: Page): Promise<Small[]> {
  return page.evaluate(() => {
    const MIN = 43.5;
    const sel = 'a[href], button, input, select, textarea, summary, [role="button"], [role="tab"], [role="menuitem"], [tabindex]:not([tabindex="-1"])';
    const out: { what: string; w: number; h: number }[] = [];
    for (const el of document.querySelectorAll<HTMLElement>(sel)) {
      if (el instanceof HTMLInputElement && el.type === "hidden") continue;
      const cs = getComputedStyle(el);
      if (cs.visibility === "hidden" || cs.display === "none" || cs.pointerEvents === "none") continue;
      if (el.closest("[inert], [hidden], dialog:not([open])")) continue;
      let box: HTMLElement = el;
      if (el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio")) box = el.closest("label") ?? el;
      const r = box.getBoundingClientRect();
      if (r.width <= 2 || r.height <= 2) continue; // visually hidden
      if (r.right <= 0 || r.bottom <= 0 || r.left >= innerWidth) continue; // off-screen
      if (el.tagName === "A" && el.closest("p, li, td, span, label, h1, h2, h3") && !el.closest("nav, header, [role=menu]")) {
        // an anchor within running text: its parent has other text next to it
        const parent = el.parentElement;
        const own = [...(parent?.childNodes ?? [])].filter((n) => n.nodeType === 3 && (n.textContent ?? "").trim() !== "").length;
        if (own > 0) continue;
      }
      if (r.width < MIN || r.height < MIN) {
        const name = (el.getAttribute("aria-label") ?? el.textContent ?? "").trim().replace(/\s+/g, " ").slice(0, 40);
        out.push({ what: `${el.tagName.toLowerCase()}${el.getAttribute("data-testid") ? `[${el.getAttribute("data-testid")}]` : ""} "${name}"`, w: Math.round(r.width), h: Math.round(r.height) });
      }
    }
    return out;
  });
}


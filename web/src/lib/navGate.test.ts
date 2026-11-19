import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { APP_ROUTES } from "./router";
import { NAV_LINKS, ROUTE_OPS, TABS, linkRoute, linkVisible, routeAllowed, tabVisible } from "./nav";

const doc = JSON.parse(readFileSync(resolve(import.meta.dirname, "../../../openapi.json"), "utf8")) as {
  paths: Record<string, Record<string, { operationId?: string }>>;
};
const opIds = new Set(Object.values(doc.paths).flatMap((p) => Object.values(p).flatMap((o) => (o.operationId ? [o.operationId] : []))));

const everything = () => true;
const nothing = () => false;
const only = (...ops: string[]) => (op: string) => ops.includes(op);

// Listener and Editor shells as the role matrix gives them (listen/rate scopes; Editor adds write).
const listener = only("search_tracks", "get_track", "get_song", "get_lineage", "list_playlists", "get_playlist", "list_crates", "get_crate", "list_records", "get_record", "get_song_atlas", "search_sounds", "compare_tracks", "rate_track");
const editor = (op: string) => listener(op) || ["list_drafts", "get_draft", "create_draft", "estimate_generation", "create_generation", "list_engines", "list_transforms"].includes(op);

describe("route gating (C313.3)", () => {
  test("every route has an entry and every named op exists in openapi.json", () => {
    for (const r of APP_ROUTES) expect(r.name in ROUTE_OPS, r.name).toBe(true);
    expect(Object.keys(ROUTE_OPS).sort()).toEqual(APP_ROUTES.map((r) => r.name).sort());
    for (const [name, op] of Object.entries(ROUTE_OPS)) if (op) expect(opIds.has(op), `${name}: ${op}`).toBe(true);
  });
  test("the Owner (every op) sees every route, link and tab: the Owner UI is unchanged", () => {
    for (const r of APP_ROUTES) expect(routeAllowed(r.name, everything), r.name).toBe(true);
    for (const l of NAV_LINKS) expect(linkVisible(l, everything), l.path).toBe(true);
    for (const t of TABS) expect(tabVisible(t, everything), t.id).toBe(true);
  });
  test("every static nav link resolves to a route", () => {
    for (const l of NAV_LINKS) expect(linkRoute(l), l.path).toBeDefined();
  });
  test("a caller who can do nothing keeps only the opted-in routes (queue, More, 404)", () => {
    const open = APP_ROUTES.filter((r) => routeAllowed(r.name, nothing)).map((r) => r.name);
    expect(open.sort()).toEqual(["more", "queue"]);
    expect(routeAllowed("nope", nothing)).toBe(true);
    expect(routeAllowed(null, nothing)).toBe(true);
  });
  test("a Listener sees the library views and no spend, approval, audit or token surface", () => {
    const shown = NAV_LINKS.filter((l) => linkVisible(l, listener)).map((l) => l.label);
    expect(shown).toEqual(["Takes", "Sounds", "Atlas", "Records", "Crates", "Playlists"]);
    for (const n of ["human-spend", "human", "human-tokens", "settings-audit", "receipts", "jobs", "board", "judge", "duels", "human-generate"]) expect(routeAllowed(n, listener), n).toBe(false);
    expect(TABS.filter((t) => tabVisible(t, listener)).map((t) => t.id)).toEqual(["new", "library", "queue", "more"]);
  });
  test("an Editor adds drafts and Generate, still no Spend, Caps, Provider, Approve or Publish", () => {
    const shown = NAV_LINKS.filter((l) => linkVisible(l, editor)).map((l) => l.label);
    expect(shown).toContain("Generate");
    expect(shown).toContain("Backlog");
    for (const gone of ["Spend", "Approvals", "Tokens", "Connectors", "Subsonic", "Audit", "Backups", "Receipts", "Duels", "Ladder"]) expect(shown).not.toContain(gone);
    expect(routeAllowed("human-record-publish", editor)).toBe(false);
    expect(routeAllowed("human-confirm", editor)).toBe(false);
  });
});

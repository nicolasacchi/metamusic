import { describe, expect, test } from "bun:test";
import { Glob } from "bun";
import { resolve } from "node:path";
import { readFileSync } from "node:fs";
import { loadDuelBadge, type BadgeClient } from "./navBadge";

describe("duel badge", () => {
  test("calls list_duels with state=open&limit=1 and shows total", async () => {
    const calls: { path: string; query: unknown }[] = [];
    const client: BadgeClient = {
      GET: (path, init) => {
        calls.push({ path, query: init.params.query });
        return Promise.resolve({ data: { items: [], total: 7, next_cursor: null } });
      },
    };
    expect(await loadDuelBadge(client)).toBe(7);
    expect(calls).toEqual([{ path: "/api/duels", query: { state: ["open"], limit: 1 } }]);
  });
  test("failures yield no badge", async () => {
    expect(await loadDuelBadge({ GET: () => Promise.reject(new Error("x")) })).toBeNull();
    expect(await loadDuelBadge({ GET: () => Promise.resolve({ error: {} }) })).toBeNull();
  });
  test("next_duel is called only by the Duels page", () => {
    const root = resolve(import.meta.dirname, "..");
    const hits: string[] = [];
    for (const f of new Glob("**/*.{ts,svelte}").scanSync({ cwd: root })) {
      if (f.endsWith(".test.ts") || f.startsWith("api/schema") || f.includes("fixtures/")) continue;
      if (readFileSync(resolve(root, f), "utf8").includes("/api/duels/next")) hits.push(f);
    }
    expect(hits).toEqual(["routes/Duels.svelte"]);
  });
});

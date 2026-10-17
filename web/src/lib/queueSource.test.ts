import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { MAX_RESTORE_PAGES, restoreMore, sourceQuery } from "./queueSource";
import type { SearchQuery } from "./libraryQuery";

type Page = components["schemas"]["TrackPage"];
const t = (id: string, at = "2026-10-01T10:00:00Z") => ({ id, recorded_at: at }) as Page["items"][number];
const pages = (...p: Page["items"][]): Page[] => p.map((items, i) => ({ items, total: 99, next_cursor: i < p.length - 1 ? `c${i + 1}` : null }));

function deps(ps: Page[], queue: string[]) {
  const calls: SearchQuery[] = [];
  return {
    calls,
    d: {
      search: async (q: SearchQuery) => {
        calls.push(q);
        return ps[q.cursor ? Number(q.cursor.slice(1)) : 0] ?? null;
      },
      seenAt: () => "2026-09-30T00:00:00Z",
      queue: () => queue,
    },
  };
}

describe("sourceQuery", () => {
  test("only /new and the Library page", () => {
    expect(sourceQuery("/new", "S")?.query).toMatchObject({ recorded_from: "S", sort: "recorded_asc" });
    expect(sourceQuery("/?status=final", "S")?.query).toMatchObject({ status: ["final"], limit: 50 });
    expect(sourceQuery("/", "S")).not.toBeNull();
    expect(sourceQuery("/songs/s1", "S")).toBeNull();
    expect(sourceQuery("/judge?from=new", "S")).toBeNull();
    expect(sourceQuery("/playlists/p", "S")).toBeNull();
  });
});

describe("restoreMore", () => {
  test("null for a source that does not page", () => {
    expect(restoreMore("/songs/s1", deps([], []).d)).toBeNull();
  });

  test("skips what the queue holds and continues page by page", async () => {
    const { d, calls } = deps(pages([t("a"), t("b")], [t("c"), t("d")], [t("e")]), ["a", "b"]);
    const more = restoreMore("/", d);
    expect(await more?.()).toEqual(["c", "d"]);
    expect(calls.map((c) => c.cursor)).toEqual([undefined, "c1"]);
    expect(await more?.()).toEqual(["e"]);
    expect(await more?.()).toEqual([]);
  });

  test("/new keeps only takes after the seen marker", async () => {
    const { d } = deps(pages([t("old", "2026-09-01T00:00:00Z"), t("new1")]), []);
    expect(await restoreMore("/new", d)?.()).toEqual(["new1"]);
  });

  test("a failed page throws so the player retries later", async () => {
    const more = restoreMore("/", { search: async () => null, seenAt: () => "", queue: () => [] });
    await expect(more?.()).rejects.toThrow();
  });

  test("gives up after MAX_RESTORE_PAGES pages that are all in the queue", async () => {
    const ps = Array.from({ length: MAX_RESTORE_PAGES + 3 }, (_, i) => [t(`x${i}`)]);
    const { d, calls } = deps(pages(...ps), ps.flat().map((x) => x.id));
    expect(await restoreMore("/", d)?.()).toEqual([]);
    expect(calls.length).toBe(MAX_RESTORE_PAGES);
  });
});

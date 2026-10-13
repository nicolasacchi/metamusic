import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import {
  addToPlaylist,
  auditFiltersFromQuery,
  auditQuery,
  copyContext,
  defaultAuditFilters,
  emptySelection,
  exportIdFromSearch,
  exportSearch,
  fetchExport,
  fromBody,
  moveItem,
  needsRefresh,
  summaryLine,
  toBody,
  unpairBrowser,
  validationView,
} from "./exportView";

type Handler = (req: Request, body: unknown) => Response | Promise<Response>;
function rig(handler: Handler) {
  const reqs: { method: string; path: string; search: string; body: unknown; headers: Headers }[] = [];
  const api = createApi({
    baseUrl: "http://localhost",
    session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {}, onLine: () => true, fetch: async () => new Response("{}") }),
    fetch: async (req) => {
      const text = req.method === "GET" ? "" : await req.clone().text();
      const body = text ? JSON.parse(text) : undefined;
      const u = new URL(req.url);
      reqs.push({ method: req.method, path: u.pathname, search: u.search, body, headers: req.headers });
      return handler(req, body);
    },
  });
  return { api, reqs };
}
const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });

describe("selection <-> request body", () => {
  test("source selection", () => {
    const f = { ...emptySelection(), kind: "source" as const, sourcePath: "music/a.jsonl", forBatch: true, filename: "out.jsonl" };
    const b = toBody(f);
    expect(b).toEqual({ selection: "source", source_path: "music/a.jsonl", for_batch: true, filename: "out.jsonl" });
    expect(fromBody(b)).toEqual(f);
  });
  test("tracks selection omits empties and parses tags", () => {
    const f = { ...emptySelection(), kind: "tracks" as const, trackStatus: ["final"], minStars: "4", tags: "a, b ,,", playlistId: "" };
    const b = toBody(f);
    expect(b).toEqual({ selection: "tracks", tracks: { status: ["final"], min_stars: 4, tags: ["a", "b"] } });
    const back = fromBody(b);
    expect(back.tags).toBe("a, b");
    expect(toBody(back)).toEqual(b);
  });
  test("drafts selection", () => {
    const f = { ...emptySelection(), kind: "drafts" as const, collection: "c1", origin: "editor", draftStatus: ["backlog"] };
    const b = toBody(f);
    expect(b).toEqual({ selection: "drafts", drafts: { status: ["backlog"], collection: "c1", origin: "editor" } });
    expect(fromBody(b)).toEqual(f);
  });
  test("for_batch only travels with source", () => {
    expect(toBody({ ...emptySelection(), kind: "tracks", forBatch: true })).toEqual({ selection: "tracks" });
  });
  test("an out-of-range or blank min stars is dropped", () => {
    expect(toBody({ ...emptySelection(), kind: "tracks", minStars: "9" })).toEqual({ selection: "tracks" });
    expect(toBody({ ...emptySelection(), kind: "tracks", minStars: "" })).toEqual({ selection: "tracks" });
  });
});

describe("summaries", () => {
  const info = { rows: 12, raw_rows: 11, generated_rows: 1 } as never;
  test("summaryLine", () => {
    expect(summaryLine(info)).toBe("12 rows · 11 raw · 1 regenerated");
    expect(summaryLine({ rows: 1, raw_rows: 1, generated_rows: 0 } as never)).toBe("1 row · 1 raw · 0 regenerated");
  });
  test("a source result with issues is the original file's, not a failure", () => {
    const v = validationView({ ok: false, fatal: false, issues: [{ code: "duplicate_id", line: 3 }] }, { kind: "source", forBatch: false });
    expect(v.tone).toBe("original");
    expect(v.label).toContain("issues of the original file");
    expect(v.label).not.toContain("failed");
  });
  test("for_batch or tracks with issues is a failure; ok is ok", () => {
    const bad = { ok: false, fatal: false, issues: [{ code: "x" }] };
    expect(validationView(bad, { kind: "source", forBatch: true }).tone).toBe("failed");
    expect(validationView(bad, { kind: "tracks", forBatch: false }).label).toContain("failed");
    const ok = validationView({ ok: true, fatal: false, issues: [] }, { kind: "tracks", forBatch: false });
    expect(ok.tone).toBe("ok");
    expect(ok.label).toBe("OK");
  });
});

describe("expiry and URL", () => {
  const exp = "2026-10-13T12:00:00Z";
  const t = Date.parse(exp);
  test("needsRefresh within one minute of expiry", () => {
    expect(needsRefresh(exp, t - 61_000)).toBe(false);
    expect(needsRefresh(exp, t - 60_000)).toBe(true);
    expect(needsRefresh(exp, t - 1_000)).toBe(true);
    expect(needsRefresh(exp, t + 5_000)).toBe(true);
    expect(needsRefresh("garbage", t)).toBe(true);
  });
  test("export id in the search string round trips", () => {
    expect(exportIdFromSearch("?export=abc&x=1")).toEqual({ id: "abc" });
    expect(exportIdFromSearch("")).toBeNull();
    const s = exportSearch("abc", { kind: "source", forBatch: true });
    expect(exportIdFromSearch(s)).toEqual({ id: "abc", kind: "source", forBatch: true });
  });
  test("?export=<id> makes exactly one get_export call", async () => {
    const { api, reqs } = rig(() => json({ id: "e1", download: { url: "/media/x", expires_at: "2099-01-01T00:00:00Z" } }));
    const r = await fetchExport(api, "e1");
    expect(r.ok).toBe(true);
    expect(reqs).toHaveLength(1);
    expect(reqs[0]!.path).toBe("/api/exports/e1");
  });
  test("410 EXPORT_EXPIRED is reported as expired", async () => {
    const { api } = rig(() => json({ code: "EXPORT_EXPIRED", message: "gone" }, 410));
    const r = await fetchExport(api, "e1");
    expect(r).toEqual({ ok: false, expired: true, message: "export expired — create it again" });
  });
});

describe("moveItem", () => {
  test("moves up and down, clamps at the ends, never mutates", () => {
    const a = ["a", "b", "c"];
    expect(moveItem(a, 1, -1)).toEqual(["b", "a", "c"]);
    expect(moveItem(a, 1, 1)).toEqual(["a", "c", "b"]);
    expect(moveItem(a, 0, -1)).toEqual(["a", "b", "c"]);
    expect(moveItem(a, 2, 1)).toEqual(["a", "b", "c"]);
    expect(moveItem(a, 7, 1)).toEqual(["a", "b", "c"]);
    expect(a).toEqual(["a", "b", "c"]);
  });
  test("duplicates are positional", () => {
    expect(moveItem(["a", "a", "b"], 2, -1)).toEqual(["a", "b", "a"]);
  });
});

describe("audit filters <-> query", () => {
  test("default excludes log_listen_events", () => {
    const q = auditQuery(defaultAuditFilters());
    expect(q.exclude_op).toEqual(["log_listen_events"]);
    expect(q.limit).toBe(50);
    expect(q.op).toBeUndefined();
  });
  test("including listen events drops the exclusion; round trip", () => {
    const f = { ...defaultAuditFilters(), includeListen: true, principalKind: ["human" as const], op: "rate_track, add_comment", statusMin: "400", since: "2026-10-01T00:00:00Z" };
    const q = auditQuery(f, "cur");
    expect(q.exclude_op).toBeUndefined();
    expect(q).toMatchObject({ principal_kind: ["human"], op: ["rate_track", "add_comment"], status_min: 400, since: "2026-10-01T00:00:00Z", cursor: "cur" });
    expect(auditFiltersFromQuery(q)).toEqual({ ...f, op: "rate_track, add_comment" });
    expect(auditFiltersFromQuery(auditQuery(defaultAuditFilters()))).toEqual(defaultAuditFilters());
  });
  test("a number from a type=number input is accepted (Svelte binds numbers, null when empty)", () => {
    const bound = (v: unknown) => ({ ...defaultAuditFilters(), statusMin: v as string });
    expect(auditQuery(bound(400)).status_min).toBe(400);
    expect(auditQuery(bound(null)).status_min).toBeUndefined();
  });
  test("bad status_min is dropped", () => {
    expect(auditQuery({ ...defaultAuditFilters(), statusMin: "7" }).status_min).toBeUndefined();
  });
});

describe("copyContext", () => {
  const bundle = (extra = "") => JSON.stringify({ format: "mm.context/1", note: "é😀" + extra }) + "\n";
  test("reports the UTF-16 length of the copied text", async () => {
    const body = bundle();
    const { api } = rig(() => new Response(body, { headers: { "content-type": "application/json" } }));
    let written = "";
    const r = await copyContext(api, "t1", { writeText: async (s) => void (written = s) });
    expect(r).toEqual({ ok: true, copied: true, chars: written.length });
    expect(written).toBe(body.trimEnd());
    // The emoji counts as two UTF-16 units.
    expect(written.length).toBe([...written].length + 1);
  });
  test("still displays the count when the clipboard refuses", async () => {
    const { api } = rig(() => new Response(bundle(), { headers: { "content-type": "application/json" } }));
    const r = await copyContext(api, "t1", {
      writeText: async () => {
        throw new Error("denied");
      },
    });
    expect(r.ok).toBe(true);
    expect(r).toMatchObject({ copied: false });
    expect(r.ok && r.chars).toBe(bundle().trimEnd().length);
  });
  test("no clipboard at all is also a refused write", async () => {
    const { api } = rig(() => new Response(bundle(), { headers: { "content-type": "application/json" } }));
    const r = await copyContext(api, "t1", undefined);
    expect(r).toMatchObject({ ok: true, copied: false });
  });
  test("a failed request is an error", async () => {
    const { api } = rig(() => json({ code: "NOT_FOUND", message: "no track" }, 404));
    expect(await copyContext(api, "t1", { writeText: async () => {} })).toEqual({ ok: false, message: "no track" });
  });
});

describe("unpairBrowser", () => {
  test("calls revoke_own_device then location.assign('/start')", async () => {
    const calls: string[] = [];
    const { api, reqs } = rig(() => json({ id: "d", name: "n", revoked_at: "2026-10-13T00:00:00Z" }));
    const ok = await unpairBrowser(api, (u) => void calls.push(u));
    expect(ok).toBe(true);
    expect(reqs.map((r) => `${r.method} ${r.path}`)).toEqual(["POST /api/devices/self/revoke"]);
    expect(calls).toEqual(["/start"]);
  });
  test("a failure stays on the page", async () => {
    const calls: string[] = [];
    const { api } = rig(() => json({ code: "NOT_A_DEVICE", message: "nope" }, 403));
    expect(await unpairBrowser(api, (u) => void calls.push(u))).toBe(false);
    expect(calls).toEqual([]);
  });
});

describe("addToPlaylist", () => {
  test("appends with the playlist revision", async () => {
    const { api, reqs } = rig(() => json({ id: "p1", revision: 4 }));
    const r = await addToPlaylist(api, { id: "p1", revision: 3 }, "t9");
    expect(r.ok).toBe(true);
    expect(reqs[0]!.method).toBe("PATCH");
    expect(reqs[0]!.body).toEqual({ revision: 3, add_track_ids: ["t9"] });
  });
  test("a revision conflict re-reads the playlist and retries once", async () => {
    let n = 0;
    const { api, reqs } = rig((req) => {
      if (req.method === "GET") return json({ id: "p1", revision: 7, items: [] });
      return ++n === 1 ? json({ code: "REVISION_CONFLICT", message: "stale" }, 409) : json({ id: "p1", revision: 8 });
    });
    const r = await addToPlaylist(api, { id: "p1", revision: 3 }, "t9");
    expect(r.ok).toBe(true);
    expect(reqs.map((q) => q.method)).toEqual(["PATCH", "GET", "PATCH"]);
    expect((reqs[2]!.body as { revision: number }).revision).toBe(7);
  });
});

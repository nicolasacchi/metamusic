import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createSession } from "../api/session";
import {
  debounced,
  emptyForm,
  formFromRequest,
  isDirty,
  issueView,
  keepMine,
  lineRange,
  lintForm,
  lintIssues,
  preview,
  saveDraft,
  toRequest,
  uploadRows,
  failedRows,
  type Timers,
} from "./draftForm";

type Handler = (req: Request, body: unknown) => Response | Promise<Response>;
function rig(handler: Handler) {
  const reqs: { method: string; path: string; body: unknown; headers: Headers }[] = [];
  const api = createApi({
    baseUrl: "http://localhost",
    session: createSession({ reload: () => {}, storage: { getItem: () => null, setItem: () => {} }, sleep: async () => {}, onLine: () => true, fetch: async () => new Response("{}") }),
    fetch: async (req) => {
      const text = req.method === "GET" ? "" : await req.clone().text();
      const body = text ? JSON.parse(text) : undefined;
      reqs.push({ method: req.method, path: new URL(req.url).pathname, body, headers: req.headers });
      return handler(req, body);
    },
  });
  return { api, reqs };
}
const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });

const form = { ...emptyForm(), id: "r1", style: "synth pop", lyrics: "[verse]\na\nb", cot: "c", seed: "42" };

describe("draftForm", () => {
  test("form and request round trip, optional fields omitted", () => {
    expect(toRequest(form)).toEqual({ id: "r1", style: "synth pop", lyrics: "[verse]\na\nb", cot: "c", seed: 42 });
    const full = { ...form, abc: "X:1", cfgScale: "1.5" };
    const req = toRequest(full);
    expect(req).toEqual({ id: "r1", style: "synth pop", lyrics: "[verse]\na\nb", cot: "c", seed: 42, abc: "X:1", cfg_scale: 1.5 });
    const back = formFromRequest(req as never, "L");
    expect(toRequest(back)).toEqual(req);
    expect(back.label).toBe("L");
    expect(formFromRequest({ id: "x", style: "s", lyrics: "", cot: "", seed: 1 }).cfgScale).toBe("");
  });

  test("preview has sorted keys", () => {
    const keys = Object.keys(JSON.parse(preview({ ...form, abc: "X:1", cfgScale: "2" })));
    expect(keys).toEqual([...keys].sort());
    expect(preview(form).indexOf('"cot"')).toBeLessThan(preview(form).indexOf('"style"'));
  });

  test("dirty detection", () => {
    expect(isDirty(form, { ...form })).toBe(false);
    expect(isDirty(form, { ...form, style: "x" })).toBe(true);
    expect(isDirty(form, { ...form, label: "n" })).toBe(true);
    expect(isDirty(form, { ...form, cfgScale: "  " })).toBe(false);
    expect(isDirty(form, { ...form, cfgScale: "1" })).toBe(true);
  });
});

describe("lint debounce", () => {
  test("three edits within 400 ms make one call", async () => {
    let now = 0;
    const pending: { at: number; fn: () => void; id: number }[] = [];
    let n = 0;
    const timers: Timers = {
      set: (fn, ms) => {
        pending.push({ at: now + ms, fn, id: ++n });
        return n;
      },
      clear: (h) => {
        const i = pending.findIndex((p) => p.id === h);
        if (i >= 0) pending.splice(i, 1);
      },
    };
    const advance = (ms: number) => {
      now += ms;
      for (const p of pending.filter((x) => x.at <= now)) {
        pending.splice(pending.indexOf(p), 1);
        p.fn();
      }
    };
    const calls: string[] = [];
    const run = debounced((s: string) => void calls.push(s), 400, timers);
    run("a");
    advance(150);
    run("ab");
    advance(150);
    run("abc");
    advance(399);
    expect(calls).toEqual([]);
    advance(1);
    expect(calls).toEqual(["abc"]);
  });

  test("lintForm posts one item with the request", async () => {
    const { api, reqs } = rig(() => json({ ok: true, batch_errors: [], items: [{ index: 0, errors: [], warnings: [] }] }));
    const r = await lintForm(api, form, "d1");
    expect(r?.index).toBe(0);
    expect(reqs).toHaveLength(1);
    expect(reqs[0]!.path).toBe("/api/recipes/lint");
    expect((reqs[0]!.body as { items: { draft_id: string }[] }).items[0]!.draft_id).toBe("d1");
  });
});

describe("conflict flow", () => {
  test("409 REVISION_CONFLICT then keep mine creates a draft with a new key", async () => {
    const detail = { id: "d2", revision: 1 };
    const { api, reqs } = rig((req) =>
      req.method === "PATCH" ? json({ code: "REVISION_CONFLICT", message: "stale", details: {} }, 409) : json(detail, 201),
    );
    const s = await saveDraft(api, "d1", 3, { ...form, label: "mine" });
    expect(s.kind).toBe("conflict");
    expect(reqs[0]!.body).toMatchObject({ revision: 3, label: "mine" });
    const k1 = await keepMine(api, { ...form, label: "mine" });
    const k2 = await keepMine(api, { ...form, label: "mine" });
    expect(k1.kind).toBe("ok");
    const posts = reqs.filter((r) => r.method === "POST");
    expect(posts).toHaveLength(2);
    expect(posts[0]!.path).toBe("/api/drafts");
    const key1 = posts[0]!.headers.get("Idempotency-Key");
    expect(key1).toBeTruthy();
    expect(posts[1]!.headers.get("Idempotency-Key")).not.toBe(key1);
    expect(k2.kind).toBe("ok");
  });

  test("other errors surface the message", async () => {
    const { api } = rig(() => json({ code: "VALIDATION", message: "bad", details: {} }, 422));
    expect(await saveDraft(api, "d1", 1, form)).toEqual({ kind: "error", message: "bad" });
  });
});

describe("lint view", () => {
  test("an issue with a line maps to the textarea line", () => {
    const v = issueView({ code: "x", message: "m", severity: "error", line: 3 }, "error");
    expect(v.line).toBe(3);
    const text = "aa\nbbb\ncc";
    const [s, e] = lineRange(text, 3);
    expect(text.slice(s, e)).toBe("cc");
    expect(text.slice(...lineRange(text, 2))).toBe("bbb");
    expect(text.slice(...lineRange(text, 99))).toBe("cc");
    expect(issueView({ code: "x", message: "m", severity: "error" }, "error").line).toBeUndefined();
  });

  test("dropped_key:<k> is a warning", () => {
    const v = issueView({ code: "dropped_key:foo", message: "", severity: "error" }, "error");
    expect(v.severity).toBe("warning");
    expect(v.droppedKey).toBe("foo");
    const all = lintIssues({
      errors: [{ code: "e", message: "m", severity: "error" }],
      warnings: [{ code: "dropped_key:bar", message: "dropped", severity: "warning" }],
    } as never);
    expect(all.map((i) => i.severity)).toEqual(["error", "warning"]);
    expect(lintIssues(null)).toEqual([]);
  });
});

describe("rejected upload rows", () => {
  test("LINT_FAILED details become per-line error rows; anything else yields none", () => {
    const body = {
      code: "LINT_FAILED",
      details: { items: [{ line: 3, errors: [{ code: "bad_json" }, { code: "bad", field: "seed" }], warnings: [] }, { line: 1, errors: [{ code: "x" }], warnings: [] }] },
    };
    expect(failedRows(body).map((r) => [r.line, r.state, r.text])).toEqual([
      [1, "error", "x"],
      [3, "error", "bad_json"],
      [3, "error", "bad (seed)"],
    ]);
    expect(failedRows({ code: "X", details: {} })).toEqual([]);
    expect(failedRows(undefined)).toEqual([]);
    expect(failedRows({ details: { items: "no" } })).toEqual([]);
  });
});

describe("upload rows", () => {
  test("per-line results merge in line order", () => {
    const rows = uploadRows({
      collection: null,
      created: 1,
      drafts: [{ id: "d", label: null, line: 1, request_id: "r1" }],
      errors: [{ code: "bad", field: "seed", line: 2 }],
      warnings: [{ code: "w", field: "", line: 1 }],
    });
    expect(rows.map((r) => [r.line, r.state])).toEqual([[1, "warning"], [1, "created"], [2, "error"]]);
    expect(rows[2]!.text).toBe("bad (seed)");
  });
});

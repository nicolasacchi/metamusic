import { describe, expect, test } from "bun:test";
import { createApi } from "../api/client";
import { createRecord, editSlots, exportTape, freezeAll, getTape, listRecords, reloadAction, updateRecord } from "./curationApi";

function rig(handler: (req: Request) => Response | Promise<Response>) {
  const seen: Request[] = [];
  const api = createApi({
    baseUrl: "http://x.test",
    fetch: async (req) => {
      seen.push(req.clone());
      return handler(req);
    },
  });
  return { api, seen };
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": status >= 400 ? "application/problem+json" : "application/json" } });
const problem = (status: number, code: string, message = "m") => json(status, { code, message, details: {} });

describe("idempotency header on the three create ops", () => {
  test("create_record sends the same key on a retry, a new key for a new action", async () => {
    const { api, seen } = rig(() => json(201, { record: { id: "r" } }));
    const act = createRecord(api, { title: "T" });
    await act();
    await act();
    const other = createRecord(api, { title: "T" });
    await other();
    const keys = seen.map((r) => r.headers.get("Idempotency-Key"));
    expect(keys[0]).toBeTruthy();
    expect(keys[0]).toBe(keys[1]!);
    expect(keys[2]).not.toBe(keys[0]!);
  });
  test("export_record_tape sends a key", async () => {
    const { api, seen } = rig(() => json(201, { id: "tp" }));
    const r = await exportTape(api, "rec-1", { revision: 4 })();
    expect(r.kind).toBe("ok");
    expect(seen[0]!.headers.get("Idempotency-Key")).toBeTruthy();
    expect(new URL(seen[0]!.url).pathname).toBe("/api/records/rec-1/tapes");
  });
});

describe("409 STALE_REVISION maps to a reload action", () => {
  test("update_record", async () => {
    const { api } = rig(() => problem(409, "STALE_REVISION", "changed"));
    const r = await updateRecord(api, "rec-1", { revision: 1, title: "x" });
    expect(r.kind).toBe("stale");
    if (r.kind === "stale") expect(r.action).toBe(reloadAction);
  });
  test("edit_record_slots", async () => {
    const { api } = rig(() => problem(409, "STALE_REVISION"));
    expect((await editSlots(api, "rec-1", 1, [{ op: "pin_all" }])).kind).toBe("stale");
  });
});

describe("501 is a value, not a throw", () => {
  test("get_tape and list_records", async () => {
    const { api } = rig(() => problem(501, "NOT_IMPLEMENTED"));
    expect((await getTape(api, "tp")).kind).toBe("unavailable");
    expect((await listRecords(api, {})).kind).toBe("unavailable");
  });
});

describe("freezeAll", () => {
  test("sends exactly one pin_all op at the given revision", async () => {
    const bodies: unknown[] = [];
    const { api } = rig(async (req) => {
      bodies.push(await req.json());
      return json(200, { record: { id: "r" } });
    });
    await freezeAll(api, "rec-1", 7);
    expect(bodies).toEqual([{ revision: 7, ops: [{ op: "pin_all" }] }]);
  });
});

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import {
  canEdit,
  canPublish,
  changeText,
  createReadyOnce,
  moveOp,
  pinControl,
  recordWarningRows,
  reorderOp,
  slotModels,
  slotWarnings,
  warningText,
  type GoldenChange,
} from "./recordView";
import { recordView, slot } from "./fixtures/l2/record";

const ids = ["a", "b", "c", "d"];

describe("reorderOp", () => {
  test("a drag result is one reorder op with the full id list", () => {
    expect(reorderOp(ids, 3, 0)).toEqual({ op: "reorder", slot_ids: ["d", "a", "b", "c"] });
    expect(reorderOp(ids, 0, 2)).toEqual({ op: "reorder", slot_ids: ["b", "c", "a", "d"] });
  });
  test("unchanged or out of range is a no-op", () => {
    expect(reorderOp(ids, 1, 1)).toBeNull();
    expect(reorderOp(ids, -1, 2)).toBeNull();
    expect(reorderOp(ids, 0, 9)).toBeNull();
  });
  test("keyboard up/down produce the same op as the equivalent drag", () => {
    expect(moveOp(ids, 2, -1)).toEqual(reorderOp(ids, 2, 1));
    expect(moveOp(ids, 1, 1)).toEqual(reorderOp(ids, 1, 2));
    expect(moveOp(ids, 0, -1)).toBeNull();
    expect(moveOp(ids, 3, 1)).toBeNull();
  });
});

describe("pinControl", () => {
  test("floating ok slot can pin; pinned can unpin", () => {
    expect(pinControl(slot(1))).toEqual({ pin: true, unpin: false, label: "Floating" });
    expect(pinControl(slot(1, { mode: "pinned" }))).toEqual({ pin: true, unpin: true, label: "Pinned" });
  });
  test("an empty slot offers neither", () => {
    const c = pinControl(slot(1, { state: "empty", take: null, pick: null }));
    expect(c.pin).toBe(false);
    expect(c.unpin).toBe(false);
  });
  test("a pinned slot whose take is not eligible says so and can only unpin", () => {
    const c = pinControl(slot(1, { mode: "pinned", state: "pin_ineligible" }));
    expect(c).toEqual({ pin: false, unpin: true, label: "Pinned, not eligible" });
  });
});

describe("warnings", () => {
  test("text per code, including undecided", () => {
    expect(warningText("undecided_pick")).toContain("undecided");
    expect(warningText("pin_ineligible")).toContain("not eligible");
    expect(warningText("empty")).toContain("no playable take");
    expect(warningText("retired_song")).toContain("merged");
    expect(warningText("something_new")).toBe("something_new");
  });
  test("slotWarnings and the record-level rows", () => {
    expect(slotWarnings(slot(1, { warnings: ["undecided_pick", "empty"] }))).toHaveLength(2);
    const v = recordView(3, { warnings: [{ slot_id: "sl-2", code: "undecided_pick" }, { slot_id: null, code: "empty" }] });
    const rows = recordWarningRows(v);
    expect(rows[0]!.text).toContain("Slot 2");
    expect(rows[0]!.text).toContain("undecided");
    expect(rows[1]!.text).not.toContain("Slot");
  });
});

describe("canEdit / canPublish", () => {
  const w = ["read", "write"];
  const rec = (status: "draft" | "proposed" | "published") => ({ status });
  test("agent, device, human by status", () => {
    expect(canEdit({ kind: "agent", scopes: w }, rec("draft"))).toBe(true);
    expect(canEdit({ kind: "agent", scopes: w }, rec("proposed"))).toBe(true);
    expect(canEdit({ kind: "agent", scopes: w }, rec("published"))).toBe(false);
    expect(canEdit({ kind: "device", scopes: w }, rec("published"))).toBe(true);
    expect(canEdit({ kind: "human", scopes: w }, rec("published"))).toBe(true);
    expect(canEdit({ kind: "human", scopes: ["read"] }, rec("draft"))).toBe(false);
    expect(canEdit(null, rec("draft"))).toBe(false);
  });
  test("only a human publishes", () => {
    expect(canPublish({ kind: "human", scopes: w })).toBe(true);
    expect(canPublish({ kind: "device", scopes: w })).toBe(false);
    expect(canPublish({ kind: "agent", scopes: w })).toBe(false);
    expect(canPublish(null)).toBe(false);
  });
});

describe("changeText mirrors the shared golden", () => {
  const rows = JSON.parse(readFileSync(new URL("./fixtures/change_text.golden.json", import.meta.url), "utf8")) as { name: string; change: GoldenChange; names: Record<string, string>; text: string }[];
  test("24 rows", () => expect(rows).toHaveLength(24));
  for (const r of rows) test(r.name, () => expect(changeText(r.change, (id) => r.names[id] ?? "")).toBe(r.text));
  test("the web golden is a byte copy of the Go one", () => {
    const go = readFileSync(new URL("../../../internal/records/testdata/change_text.golden.json", import.meta.url), "utf8");
    const web = readFileSync(new URL("./fixtures/change_text.golden.json", import.meta.url), "utf8");
    expect(web).toBe(go);
  });
  test("generic forms never throw", () => {
    expect(changeText({ kind: "slot_pinned", position: 1, to_take_id: "x" } as GoldenChange, null)).toBe("Slot 1 pinned to a take");
    expect(changeText({ kind: "nope" } as GoldenChange, null)).toBe("Record changed");
  });
});

describe("slotModels", () => {
  test("a 60-slot model builds in under 20 ms", () => {
    const v = recordView(60);
    const t0 = performance.now();
    const m = slotModels(v);
    expect(performance.now() - t0).toBeLessThan(20);
    expect(m).toHaveLength(60);
    expect(m[0]!.canUp).toBe(false);
    expect(m[0]!.canDown).toBe(true);
    expect(m[59]!.canDown).toBe(false);
  });
  test("pin differs and undecided flags surface", () => {
    const v = recordView(2);
    v.slots[0] = slot(1, { pin_differs: true, mode: "pinned", why: "newest take · undecided", warnings: ["undecided_pick"] });
    const m = slotModels(v);
    expect(m[0]!.pinDiffers).toBe(true);
    expect(m[0]!.undecided).toBe(true);
    expect(m[1]!.undecided).toBe(false);
  });
});

describe("createReadyOnce", () => {
  test("fires once, only after the first load", () => {
    let n = 0;
    const r = createReadyOnce(() => n++);
    r.loaded(false);
    expect(n).toBe(0);
    r.loaded(true);
    r.loaded(true);
    expect(n).toBe(1);
  });
});

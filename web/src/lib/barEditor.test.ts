import { describe, expect, test } from "bun:test";
import { buildEdit, editRefusalText, emptyEdit, violationsOf, type EditForm } from "./deskView";
import BarEditor from "../lib/components/BarEditor.svelte";
import { render } from "svelte/server";

const form = (o: Partial<EditForm>): EditForm => ({ ...emptyEdit("1"), ...o });
const fail = (code: string, details: Record<string, unknown>, status = 422) => ({ ok: false as const, status, code, message: "server text", details });

describe("buildEdit", () => {
  test("replace_bars: bars are 1-based in the form, 0-based on the wire, the text is trimmed", () => {
    const b = buildEdit(form({ mode: "replace", from: "2", to: "3", abc: " CDEF|G4| ", rationale: " brighter " }), 8);
    expect(b.errors).toEqual({});
    expect(b.ops).toEqual([{ op: "replace_bars", voice: "1", bar_from: 1, bar_to: 2, abc: "CDEF|G4|" }]);
    expect(b.rationale).toBe("brighter");
  });
  test("transpose_bars: a blank voice means every voice and is left out; semitones may be negative", () => {
    expect(buildEdit(form({ mode: "transpose", voice: "", from: "1", to: "1", semitones: "-2" }), 4).ops).toEqual([{ op: "transpose_bars", bar_from: 0, bar_to: 0, semitones: -2 }]);
    expect(buildEdit(form({ mode: "transpose", voice: "2", from: "1", to: "4", semitones: "12" }), 4).ops).toEqual([{ op: "transpose_bars", voice: "2", bar_from: 0, bar_to: 3, semitones: 12 }]);
  });
  test("set_tempo", () => {
    expect(buildEdit(form({ mode: "tempo", bpm: "96" }), 4).ops).toEqual([{ op: "set_tempo", bpm: 96 }]);
  });
  test.each([
    ["replace without a voice", { mode: "replace", voice: "", from: "1", to: "1", abc: "C4|" }, "voice"],
    ["replace without text", { mode: "replace", from: "1", to: "1", abc: "  " }, "abc"],
    ["bar 0", { mode: "replace", from: "0", to: "1", abc: "C4|" }, "from"],
    ["bar past the end", { mode: "replace", from: "1", to: "9", abc: "C4|" }, "to"],
    ["last before first", { mode: "replace", from: "3", to: "2", abc: "C4|" }, "to"],
    ["not a number", { mode: "replace", from: "x", to: "1", abc: "C4|" }, "from"],
    ["zero semitones", { mode: "transpose", from: "1", to: "1", semitones: "0" }, "semitones"],
    ["too many semitones", { mode: "transpose", from: "1", to: "1", semitones: "25" }, "semitones"],
    ["tempo too low", { mode: "tempo", bpm: "19" }, "bpm"],
    ["tempo too high", { mode: "tempo", bpm: "301" }, "bpm"],
    ["tempo blank", { mode: "tempo", bpm: "" }, "bpm"],
    ["rationale too long", { mode: "tempo", bpm: "90", rationale: "x".repeat(1001) }, "rationale"],
  ] as [string, Partial<EditForm>, string][])("%s is refused before any call", (_n, f, key) => {
    const b = buildEdit(form(f), 8);
    expect(b.ops).toBeNull();
    expect(b.errors[key]).toBeTruthy();
  });
  test("selecting a bar prefills the form", () => {
    const f = emptyEdit("2", 4);
    expect([f.voice, f.from, f.to]).toEqual(["2", "5", "5"]);
  });
});

describe("refusals", () => {
  test("names the rule in words; the server's own text is the fallback", () => {
    expect(editRefusalText(fail("PLAN_NO_CHANGE", {}))).toContain("leaves the score");
    expect(editRefusalText(fail("PLAN_EDIT_INVALID", { rule: "bad_voice" }))).toContain("voice");
    expect(editRefusalText(fail("PLAN_EDIT_INVALID", { rule: "result_unparsable", line: 7 }))).toContain("(line 7)");
    expect(editRefusalText(fail("PLAN_EDIT_INVALID", { rule: "brand_new" }))).toBe("server text");
    expect(editRefusalText(fail("FORBIDDEN", {}, 403))).toContain("cannot edit");
    expect(editRefusalText(fail("NOT_FOUND", {}, 404))).toContain("no longer");
    expect(editRefusalText(fail("X", {}, 500))).toBe("server text");
  });
  test("violations are listed with 1-based bars", () => {
    const v = violationsOf(fail("PLAN_EDIT_INVALID", { rule: "invariants", violations: [{ code: "bar_length", voice: "1", bar: 2, msg: "short" }, "junk", { msg: "x" }] }));
    expect(v).toEqual([{ code: "bar_length", voice: "1", bar: 3, msg: "short" }, { code: "", voice: "", bar: 1, msg: "x" }]);
    expect(violationsOf(fail("X", {}))).toEqual([]);
  });
});

describe("BarEditor markup", () => {
  const plan = { id: "p1", abc: "X:1\nK:C\nCDEF|]\n", bar_spans: [], bars: 1, score: { voices: [{ id: "1", bars: 1, name: null }] }, sections: [] };
  const html = (props: Record<string, unknown>) => render(BarEditor as never, { props: { plan, onedited: () => {}, ...props } } as never).body;
  test("offers the three edits and the voices of the plan", () => {
    const h = html({});
    for (const t of ["edit-mode-replace", "edit-mode-transpose", "edit-mode-tempo", "edit-apply", "edit-voice"]) expect(h).toContain(`data-testid="${t}"`);
    expect(h).toContain('<option value="1">1</option>');
    expect(h).toContain("Click a note in the score");
  });
  test("a selected bar is named", () => {
    expect(html({ selected: { voice: "1", bar: 2 } })).toContain("Bar 3 of voice 1 is selected.");
  });
});

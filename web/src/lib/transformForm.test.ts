import { describe, expect, test } from "bun:test";
import { capsKinds } from "./fixtures/t1/transforms";
import { buildRequest, defaultForm, fieldsFor, isAuto, kindOffered, labelFor, type FormState } from "./transformForm";

const caps = (k: string) => capsKinds.find((c) => c.kind === k)!;
const ctx = { trackId: "t-here" };
const form = (kind: string, values: Record<string, string> = {}, title = ""): FormState => ({ ...defaultForm(kind), title, values });

describe("fields are generated from the capabilities table", () => {
  test("glacier: one numeric field per table row, ranges and defaults carried, no id picker", () => {
    const f = fieldsFor(caps("glacier"));
    expect(f.map((x) => x.key)).toEqual(["factor", "start_s", "length_s", "window_s", "fade_in_s", "fade_out_s"]);
    expect(f[0]).toMatchObject({ type: "number", label: "Factor", min: 10, max: 50, placeholder: "20", required: false });
    expect(f.find((x) => x.key === "fade_in_s")?.label).toBe("Fade in (s)");
  });
  test("morph: partner picker, bar positions, downbeat fields; the A id is the page's Take", () => {
    const f = fieldsFor(caps("morph"));
    expect(f.map((x) => x.key)).toEqual(["track_b_id", "bars", "a_at_bar", "b_at_bar", "bpm_a", "bpm_b", "beats_per_bar", "downbeat_a_s", "downbeat_b_s", "key_policy", "semitones_b"]);
    expect(f.find((x) => x.key === "downbeat_a_s")?.type).toBe("downbeat");
    expect(f.find((x) => x.key === "key_policy")).toMatchObject({ type: "select", options: ["to_a", "none"] });
    expect(f.find((x) => x.key === "bars")).toMatchObject({ type: "integer", step: 1 });
  });
  test("ensemble space is a preset list; dry level has its own control", () => {
    const f = fieldsFor(caps("ensemble"));
    expect(f.find((x) => x.key === "space")?.options).toEqual(["cathedral", "cistern", "hangar", "canyon"]);
    expect(f.find((x) => x.key === "dry_db")?.type).toBe("dryDb");
  });
  test("hi-fi kinds are generated too, binaural keeps its enum", () => {
    expect(fieldsFor(caps("master")).map((x) => x.key)).toContain("ceiling_dbtp");
    expect(fieldsFor(caps("master")).find((x) => x.key === "ceiling_dbtp")?.label).toBe("Ceiling (dBTP)");
    expect(fieldsFor(caps("widen")).map((x) => x.key)).toEqual(["width", "decorrelate", "bass_mono_hz"]);
    expect(fieldsFor(caps("binaural")).find((x) => x.key === "model")?.options).toEqual(["hrtf", "parametric"]);
  });
  test("labels", () => {
    expect(labelFor("bpm_a")).toBe("BPM a");
    expect(labelFor("target_lufs")).toBe("Target (LUFS)");
    expect(labelFor("elevation_deg")).toBe("Elevation (°)");
  });
});

describe("request bodies (golden)", () => {
  test("glacier with defaults left blank sends null for each and the page's Take as source", () => {
    const b = buildRequest(caps("glacier"), form("glacier"), ctx);
    expect(b.errors).toEqual({});
    expect(b.body).toEqual({
      kind: "glacier",
      title: null,
      glacier: { source_id: "t-here", factor: null, start_s: null, length_s: null, window_s: null, fade_in_s: null, fade_out_s: null },
    });
  });
  test("glacier with values and a title", () => {
    const b = buildRequest(caps("glacier"), form("glacier", { factor: "30", fade_in_s: "6.5" }, " Slow "), ctx);
    expect(b.body).toMatchObject({ title: "Slow", glacier: { source_id: "t-here", factor: 30, fade_in_s: 6.5, start_s: null } });
  });
  test("morph: partner, bar position, downbeat number and auto, enum", () => {
    const b = buildRequest(caps("morph"), form("morph", { track_b_id: " t-b ", a_at_bar: "16", downbeat_a_s: "0.52", downbeat_b_s: "auto", key_policy: "none", bars: "12" }), ctx);
    expect(b.errors).toEqual({});
    expect(b.body as unknown).toEqual({
      kind: "morph",
      title: null,
      morph: {
        track_a_id: "t-here",
        track_b_id: "t-b",
        bars: 12,
        a_at: { bar: 16 },
        bpm_a: null,
        bpm_b: null,
        beats_per_bar: null,
        downbeat_a_s: 0.52,
        downbeat_b_s: "auto",
        key_policy: "none",
        semitones_b: null,
      },
    });
  });
  test("ensemble: dry_db blank is absent, 'off' is null (minus infinity), a number is a number", () => {
    const body = (v: string) => (buildRequest(caps("ensemble"), form("ensemble", { dry_db: v, space: "canyon" }), ctx).body as unknown as { ensemble: Record<string, unknown> }).ensemble;
    expect("dry_db" in body("")).toBe(false);
    expect(body("off").dry_db).toBeNull();
    expect(body("-6").dry_db).toBe(-6);
    expect(body("").space).toBe("canyon");
  });
  test("master, widen and binaural carry their own member", () => {
    const m = buildRequest(caps("master"), form("master", { preset: "warm", low_cut_hz: "30" }), ctx).body as unknown as Record<string, Record<string, unknown>>;
    expect(m.master).toMatchObject({ source_id: "t-here", preset: "warm", low_cut_hz: 30, ceiling_dbtp: null });
    const w = buildRequest(caps("widen"), form("widen", { width: "1.5" }), ctx).body as unknown as Record<string, Record<string, unknown>>;
    expect(w.widen).toEqual({ source_id: "t-here", width: 1.5, decorrelate: null, bass_mono_hz: null });
    const bn = buildRequest(caps("binaural"), form("binaural"), ctx).body as unknown as Record<string, Record<string, unknown>>;
    expect(bn.binaural).toEqual({ source_id: "t-here", model: null, elevation_deg: null });
  });
  test("score_render sends this Take as the score source", () => {
    const sc = { kind: "score_render", params: [{ name: "score", type: "object", required: true }, { name: "tail_s", type: "number", required: false, min: 0, max: 10 }] };
    expect(buildRequest(sc, form("score_render"), ctx).body).toEqual({ kind: "score_render", title: null, score_render: { score: { from_track_id: "t-here" }, tail_s: null } });
  });
});

describe("validation", () => {
  test("out of range, not a number, fraction for an integer, unknown enum", () => {
    const b = buildRequest(caps("morph"), form("morph", { track_b_id: "t-b", bars: "40", bpm_a: "abc", beats_per_bar: "4.5", key_policy: "weird", a_at_bar: "-1" }), ctx);
    expect(b.body).toBeNull();
    expect(b.errors).toEqual({
      bars: "Must be 1–32",
      bpm_a: "Enter a number",
      beats_per_bar: "Enter a whole number",
      key_policy: "Choose one of the listed values",
      a_at_bar: "Must be at least 0",
    });
  });
  test("morph needs a partner other than the page's Take", () => {
    expect(buildRequest(caps("morph"), form("morph"), ctx).errors).toEqual({ track_b_id: "Choose a take" });
    expect(buildRequest(caps("morph"), form("morph", { track_b_id: "t-here" }), ctx).errors).toEqual({ track_b_id: "Choose a different take" });
  });
  test("a required downbeat is an error until typed or auto", () => {
    const c = { kind: "polytempo", params: [{ name: "track_b_id", type: "string", required: true }, { name: "downbeat_a_s", type: "number", required: true, min: 0 }] };
    expect(buildRequest(c, form("polytempo", { track_b_id: "x" }), ctx).errors).toEqual({ downbeat_a_s: "Enter the downbeat or choose auto" });
    expect(buildRequest(c, form("polytempo", { track_b_id: "x", downbeat_a_s: "auto" }), ctx).body).not.toBeNull();
  });
  test("downbeat seconds are not capped by the table (the server checks the input length)", () => {
    const b = buildRequest(caps("morph"), form("morph", { track_b_id: "t-b", downbeat_a_s: "900" }), ctx);
    expect(b.errors).toEqual({});
  });
  test("auto toggle helper", () => {
    expect(isAuto(form("morph", { downbeat_a_s: "auto" }), "downbeat_a_s")).toBe(true);
    expect(isAuto(form("morph", { downbeat_a_s: "1" }), "downbeat_a_s")).toBe(false);
  });
});

describe("kinds offered", () => {
  test("score render needs the server to list it and a score on this Take", () => {
    expect(kindOffered("glacier", ["glacier"], ctx)).toBe(true);
    expect(kindOffered("glacier", ["morph"], ctx)).toBe(false);
    expect(kindOffered("score_render", ["score_render"], ctx)).toBe(false);
    expect(kindOffered("score_render", ["score_render"], { ...ctx, hasScore: true })).toBe(true);
  });
});

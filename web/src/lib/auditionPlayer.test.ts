import { describe, expect, test } from "bun:test";
import { barAt, barStarts, auditionBody, auditionRefusalText, auditionLine, clock, defaultAuditionForm, playOutcome, type AuditionForm } from "./auditionPlayer";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve } from "node:path";

const span = (voice: string, bar: number, start_s: number | null) => ({ voice, bar, start: bar * 4, end: bar * 4 + 3, start_s });
const spans = [span("1", 0, 0), span("1", 1, 2), span("1", 2, 4.5), span("2", 0, 0), span("2", 1, 2)];

describe("bar timing", () => {
  test("barStarts takes the first voice that has times, in bar order", () => {
    expect(barStarts(spans, ["1", "2"]).map((b) => b.bar)).toEqual([0, 1, 2]);
    expect(barStarts([span("1", 0, null), span("2", 0, 0), span("2", 1, 3)], ["1", "2"])).toEqual([
      { voice: "2", bar: 0, start_s: 0 },
      { voice: "2", bar: 1, start_s: 3 },
    ]);
    expect(barStarts([], ["1"])).toEqual([]);
    expect(barStarts([span("1", 0, null)], ["1"])).toEqual([]);
  });
  test("barAt is the last bar that has started; before the first start, none", () => {
    const b = barStarts(spans, ["1", "2"]);
    expect(barAt(b, 0)).toEqual({ voice: "1", bar: 0 });
    expect(barAt(b, 1.99)).toEqual({ voice: "1", bar: 0 });
    expect(barAt(b, 2)).toEqual({ voice: "1", bar: 1 });
    expect(barAt(b, 99)).toEqual({ voice: "1", bar: 2 });
    expect(barAt(b, -1)).toBeNull();
    expect(barAt([], 3)).toBeNull();
  });
});

describe("the request", () => {
  const f = (o: Partial<AuditionForm> = {}): AuditionForm => ({ ...defaultAuditionForm, ...o });
  test("defaults send nothing: the server's own soundfont, chords, tail and gain", () => {
    expect(auditionBody(f())).toEqual({});
  });
  test("chosen values are sent; blank and bad numbers are not", () => {
    expect(auditionBody(f({ soundfont: "gm", chords: "none", tail: "2", gain: "0.8" }))).toEqual({ soundfont: "gm", chords: "none", tail_s: 2, gain: 0.8 });
    expect(auditionBody(f({ tail: "x", gain: " " }))).toEqual({});
  });
});

describe("words", () => {
  const fail = (status: number, code: string, details: Record<string, unknown> = {}) => ({ ok: false as const, status, code, message: "m", details });
  test("each refusal the server can give", () => {
    expect(auditionRefusalText(fail(409, "AUDITION_BUSY", { retry_after_s: 10 }))).toMatch(/busy.*10 s/i);
    expect(auditionRefusalText(fail(429, "AUDITION_LIMIT", { limit: 200 }))).toMatch(/200/);
    expect(auditionRefusalText(fail(422, "AUDITION_TOO_LONG", { seconds: 700, max: 600 }))).toMatch(/too long/i);
    expect(auditionRefusalText(fail(422, "SOUNDFONT_UNKNOWN", {}))).toMatch(/soundfont/i);
    expect(auditionRefusalText(fail(422, "SCORE_INVALID", { rule: "empty", line: 3, col: 2 }))).toMatch(/line 3/);
    expect(auditionRefusalText(fail(503, "UNAVAILABLE", { component: "fluidsynth" }))).toMatch(/not available/i);
    expect(auditionRefusalText(fail(500, "AUDITION_FAILED"))).toMatch(/could not be made/i);
    expect(auditionRefusalText(fail(0, "NETWORK"))).toBe("m");
  });
  test("line and clock", () => {
    expect(auditionLine({ soundfont: "gm", chords: "block", seconds: 62.4 })).toBe("gm · chords · 1:02");
    expect(auditionLine({ soundfont: "gm", chords: "none", seconds: 9 })).toBe("gm · notes only · 0:09");
    expect(clock(0)).toBe("0:00");
    expect(clock(125.9)).toBe("2:05");
    expect(clock(Number.NaN)).toBe("0:00");
  });
  test("playOutcome: an expired link is retried once with a fresh one; gone is final", () => {
    expect(playOutcome(new Error("Clip link expired"), 0)).toBe("retry");
    const exp = Object.assign(new Error("x"), { name: "ClipLinkExpired" });
    expect(playOutcome(exp, 0)).toBe("retry");
    expect(playOutcome(exp, 1)).toBe("fail");
    expect(playOutcome(Object.assign(new Error("x"), { name: "ClipGone" }), 0)).toBe("fail");
  });
});

describe("the page never makes a second audio element or a second synth", () => {
  function files(dir: string, out: string[] = []): string[] {
    for (const n of readdirSync(dir)) {
      const p = join(dir, n);
      if (statSync(p).isDirectory()) files(p, out);
      else if (/\.(svelte|ts)$/.test(n) && !n.endsWith(".test.ts")) out.push(p);
    }
    return out;
  }
  test("AuditionPlayer plays through the shared AudioContext and creates no audio element", () => {
    const src = readFileSync(resolve(import.meta.dirname, "components/AuditionPlayer.svelte"), "utf8");
    expect(src).toContain("getAudioContext");
    expect(src).toContain("acquireFocus");
    expect(src).not.toMatch(/<audio|new Audio\(|createElement\(["']audio/);
    for (const f of files(resolve(import.meta.dirname, ".."))) expect(readFileSync(f, "utf8")).not.toMatch(/CreateSynth/);
  });
});

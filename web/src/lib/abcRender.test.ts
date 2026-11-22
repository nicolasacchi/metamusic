import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { barAtChar, markBar, renderScore, spansInChars, type AbcLib } from "./abcRender";
import type { PlanBarSpan } from "./deskApi";

const ABC = "X:1\nT:Toy\nM:4/4\nL:1/4\nK:C\nCDEF|G4|]\n";
const span = (voice: string, bar: number, start: number, end: number): PlanBarSpan => ({ voice, bar, start, end, start_s: null });
const at = ABC.indexOf("CDEF");
const spans = [span("1", 0, at, at + 4), span("1", 1, at + 5, at + 7)];

describe("barAtChar", () => {
  test("a position inside a bar names its voice and bar; a header or a bar line names none", () => {
    expect(barAtChar(spans, at)).toEqual({ voice: "1", bar: 0 });
    expect(barAtChar(spans, at + 3)).toEqual({ voice: "1", bar: 0 });
    expect(barAtChar(spans, at + 4)).toBeNull(); // the bar line
    expect(barAtChar(spans, at + 5)).toEqual({ voice: "1", bar: 1 });
    expect(barAtChar(spans, 0)).toBeNull();
  });
  test("with two voices the position decides, not the bar number", () => {
    const two = [span("1", 0, 10, 14), span("2", 0, 20, 24)];
    expect(barAtChar(two, 21)).toEqual({ voice: "2", bar: 0 });
  });
});

describe("spansInChars", () => {
  test("ASCII is unchanged", () => {
    expect(spansInChars(ABC, spans)).toEqual(spans);
  });
  test("byte offsets after an accented title become UTF-16 positions", () => {
    const abc = "X:1\nT:Café\nK:C\nCDEF|G4|]\n";
    const bytePos = new TextEncoder().encode(abc.slice(0, abc.indexOf("CDEF"))).length;
    expect(bytePos).toBe(abc.indexOf("CDEF") + 1);
    const out = spansInChars(abc, [span("1", 0, bytePos, bytePos + 4)]);
    expect(out[0]!.start).toBe(abc.indexOf("CDEF"));
    expect(abc.slice(out[0]!.start, out[0]!.end)).toBe("CDEF");
  });
  test("an astral character (two UTF-16 units, four bytes) is counted once per unit", () => {
    const abc = "X:1\nT:\u{1d11e}\nK:C\nCDEF|]\n";
    const bytePos = new TextEncoder().encode(abc.slice(0, abc.indexOf("CDEF"))).length;
    const out = spansInChars(abc, [span("1", 0, bytePos, bytePos + 4)]);
    expect(abc.slice(out[0]!.start, out[0]!.end)).toBe("CDEF");
  });
});

describe("renderScore", () => {
  test("draws with classes and responsive sizing, and a note click reports its bar", async () => {
    let params: Record<string, unknown> = {};
    let drawn = "";
    const lib: AbcLib = {
      renderAbc: (_t, abc, p) => {
        drawn = abc;
        params = p;
        return [];
      },
    };
    const got: unknown[] = [];
    await renderScore({} as HTMLElement, { abc: ABC, spans, onBar: (b) => got.push(b), load: () => Promise.resolve(lib) });
    expect(drawn).toBe(ABC);
    expect(params.add_classes).toBe(true);
    expect(params.responsive).toBe("resize");
    const click = params.clickListener as (e: { startChar?: number }) => void;
    click({ startChar: at + 1 });
    click({ startChar: 0 }); // a header: no bar
    click({}); // no position
    expect(got).toEqual([{ voice: "1", bar: 0 }]);
  });
  test("a loader that fails rejects, so the page can say the notation did not load", async () => {
    await expect(renderScore({} as HTMLElement, { abc: ABC, spans, load: () => Promise.reject(new Error("offline")) })).rejects.toThrow("offline");
  });
});

describe("markBar", () => {
  test("marks the notes of the selected voice and measure and clears the previous mark", () => {
    const cls = new Set<string>();
    const el = (c: string) => ({ classList: { add: (x: string) => cls.add(`${c}+${x}`), remove: (x: string) => cls.delete(`${c}+${x}`) } });
    const sel: string[] = [];
    const root = {
      querySelectorAll: (q: string) => {
        sel.push(q);
        return q === ".mm-bar-selected" ? [] : [el("n1"), el("n2")];
      },
    };
    markBar(root as unknown as ParentNode, ["1", "2"], { voice: "2", bar: 3 });
    expect(sel).toContain(".abcjs-v1.abcjs-mm3");
    expect(cls.size).toBe(2);
    sel.length = 0;
    markBar(root as unknown as ParentNode, ["1", "2"], null);
    expect(sel).toEqual([".mm-bar-selected"]);
  });
});

describe("abcjs is notation only (D-DESK-17)", () => {
  const src = resolve(import.meta.dirname, "..");
  const files: string[] = [];
  const walk = (d: string) => {
    for (const n of readdirSync(d)) {
      const p = join(d, n);
      if (statSync(p).isDirectory()) walk(p);
      else if (/\.(ts|svelte)$/.test(n) && !n.endsWith(".test.ts") && !p.includes(`${join("src", "api")}${"/"}schema`)) files.push(p);
    }
  };
  walk(src);
  test("no module uses the abcjs synthesizer", () => {
    expect(files.length).toBeGreaterThan(50);
    for (const f of files) expect(readFileSync(f, "utf8"), f).not.toMatch(/CreateSynth|SynthController|abcjs\/synth|abcjs-audio|\.synth\b|\bsynth\./);
  });
  test("only abcRender.ts imports abcjs, and only dynamically", () => {
    for (const f of files) {
      const t = readFileSync(f, "utf8");
      const imports = t.match(/(?:from\s+|import\s*\(\s*)["']abcjs["']/g) ?? [];
      if (f.endsWith("abcRender.ts")) {
        expect(imports.length).toBe(1);
        expect(t).toMatch(/import\("abcjs"\)/);
        expect(t).not.toMatch(/from\s+["']abcjs["']/);
      } else expect(imports, f).toEqual([]);
    }
  });
  test("abcjs is pinned exactly in package.json", () => {
    const pkg = JSON.parse(readFileSync(resolve(import.meta.dirname, "../../package.json"), "utf8")) as { dependencies: Record<string, string> };
    expect(pkg.dependencies.abcjs).toMatch(/^\d+\.\d+\.\d+$/);
  });
});

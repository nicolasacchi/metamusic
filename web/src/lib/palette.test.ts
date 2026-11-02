import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { OTHER_INDEX, PALETTE_SIZE, paletteVar } from "./palette";

const css = readFileSync(join(import.meta.dirname, "..", "styles", "tokens.css"), "utf8");

function token(name: string): string {
  const m = new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})\\b`).exec(css);
  if (!m) throw new Error(`token --${name} is not a #rrggbb literal`);
  return m[1]!.toLowerCase();
}
function lum(hex: string): number {
  const c = [1, 3, 5].map((i) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!;
}
function contrast(a: string, b: string): number {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

const names = [...Array.from({ length: PALETTE_SIZE }, (_, i) => paletteVar(i)), paletteVar(OTHER_INDEX)].map((v) => v.replace(/^var\(--|\)$/g, ""));

test("12 categorical colours plus other, all distinct", () => {
  expect(PALETTE_SIZE).toBe(12);
  const hex = names.map(token);
  expect(new Set(hex).size).toBe(13);
});

for (const bg of ["bg", "surface"]) {
  test(`every palette colour >= 3:1 on --${bg}`, () => {
    for (const n of names) expect(contrast(token(n), token(bg))).toBeGreaterThanOrEqual(3);
  });
}

test("paletteVar: rank >= 12 and negatives fall to other", () => {
  expect(paletteVar(0)).toBe("var(--atlas-1)");
  expect(paletteVar(11)).toBe("var(--atlas-12)");
  expect(paletteVar(12)).toBe("var(--atlas-other)");
  expect(paletteVar(99)).toBe("var(--atlas-other)");
  expect(paletteVar(-1)).toBe("var(--atlas-other)");
});

import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const css = readFileSync(join(import.meta.dirname, "tokens.css"), "utf8");

function token(name: string): string {
  const m = new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})\\b`).exec(css);
  if (!m) throw new Error(`token --${name} is not a #rrggbb literal`);
  return m[1]!;
}

function lum(hex: string): number {
  const c = [1, 3, 5].map((i) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!;
}

export function contrast(a: string, b: string): number {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

test("dark only: color-scheme dark, no light block or theme switch", () => {
  expect(css).toMatch(/color-scheme:\s*dark;/);
  expect(css).not.toMatch(/prefers-color-scheme|data-theme|color-scheme:[^;]*light/);
});

for (const fg of ["text", "text-2", "accent"]) {
  for (const bg of ["bg", "surface"]) {
    test(`--${fg} on --${bg} >= 4.5`, () => {
      expect(contrast(token(fg), token(bg))).toBeGreaterThanOrEqual(4.5);
    });
  }
}

// --text-3 is the tertiary tone (section labels, placeholders). It must still reach AA on --bg;
// on --surface it is only for decorative/non-essential text.
test("--text-3 on --bg >= 4.5", () => {
  expect(contrast(token("text-3"), token("bg"))).toBeGreaterThanOrEqual(4.5);
});

for (const bg of ["bg", "surface"]) {
  test(`--line-strong on --${bg} >= 3 (non-text UI boundary)`, () => {
    expect(contrast(token("line-strong"), token(bg))).toBeGreaterThanOrEqual(3);
  });
}

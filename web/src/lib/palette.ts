// The Atlas categorical palette (C145.7): 12 colours plus "other", all defined as CSS custom properties in
// styles/tokens.css (the only place a colour literal may live). Colours are referenced as `var(--atlas-N)`.

export const PALETTE_SIZE = 12;
export const OTHER_INDEX = PALETTE_SIZE;

/** CSS value for a colour index; any index outside 0..11 is "other". */
export function paletteVar(c: number): string {
  return Number.isInteger(c) && c >= 0 && c < PALETTE_SIZE ? `var(--atlas-${c + 1})` : "var(--atlas-other)";
}

/** Resolved colours (12 + other) for canvas drawing, read from the live tokens on `el`. */
export function readPalette(el: Element): string[] {
  const cs = getComputedStyle(el);
  const out: string[] = [];
  for (let i = 1; i <= PALETTE_SIZE; i++) out.push(cs.getPropertyValue(`--atlas-${i}`).trim());
  out.push(cs.getPropertyValue("--atlas-other").trim());
  return out;
}

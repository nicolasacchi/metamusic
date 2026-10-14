// Pure money helpers. Money is integer micro-USD (architecture rule 6); no float math is used on amounts.
// formatMicroUSD mirrors Go's money.MicroUSD.Display, formatMicroUSDCeil its DisplayCeil (D-M3-29);
// both are checked against the shared table money.cases.json.

function fmt(micro: number, ceil: boolean): string {
  if (!Number.isFinite(micro) || micro === 0) return "$0.00";
  const neg = micro < 0;
  const a = BigInt(Math.trunc(Math.abs(micro)));
  if (a === 0n) return "$0.00";
  const big = a >= 10_000n;
  const unit = big ? 10_000n : 100n;
  const dec = big ? 2 : 4;
  const add = ceil ? unit - 1n : unit / 2n;
  let s = ((a + add) / unit).toString();
  while (s.length <= dec) s = "0" + s;
  return (neg ? "-" : "") + "$" + s.slice(0, s.length - dec) + "." + s.slice(s.length - dec);
}

/** "$0.33" for amounts of at least $0.01, four decimals below that, half-up. */
export function formatMicroUSD(micro: number): string {
  return fmt(micro, false);
}

/** Rounded up (in magnitude): every "up to" amount (estimate ceiling, the Confirm button). */
export function formatMicroUSDCeil(micro: number): string {
  return fmt(micro, true);
}

const MAX_MICRO = Number.MAX_SAFE_INTEGER;

/** Parses a dollar amount typed by the user ("0.50") into micro-USD; null when invalid (more than 6 decimals, signs, exponents). */
export function parseUSDInput(text: string): number | null {
  const m = /^(\d+)(?:\.(\d{1,6}))?$/.exec(text.trim());
  if (!m) return null;
  const micro = BigInt(m[1]!) * 1_000_000n + BigInt((m[2] ?? "").padEnd(6, "0") || "0");
  return micro > BigInt(MAX_MICRO) ? null : Number(micro);
}

/** The editable text of a micro-USD amount for a dollar input ("0.5", "12"); exact, no float math. */
export function microToInput(micro: number): string {
  const a = BigInt(Math.max(0, Math.trunc(micro)));
  const whole = a / 1_000_000n;
  const frac = (a % 1_000_000n).toString().padStart(6, "0").replace(/0+$/, "");
  return frac ? `${whole}.${frac}` : `${whole}`;
}

// Blind-round pair selection (C54.4): pure, the RNG is injected.
export const BLIND_ROUND_MAX_PAIRS = 15;
/** Up to this many tracks every pair is played (6 tracks = 15 pairs). */
export const BLIND_ROUND_ALL_PAIRS_MAX_TRACKS = 6;

export type Pair = [string, string];

export function canBlindRound(trackCount: number): boolean {
  return trackCount >= 2;
}

/** Fisher-Yates on a copy. */
function shuffle<T>(xs: readonly T[], rng: () => number): T[] {
  const a = xs.slice();
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.min(i, Math.floor(rng() * (i + 1)));
    [a[i], a[j]] = [a[j]!, a[i]!];
  }
  return a;
}

/** Every pair for <= 6 tracks, else 15 random distinct pairs; always in random order. */
export function blindRoundPairs(trackIds: readonly string[], rng: () => number): Pair[] {
  const ids = [...new Set(trackIds)];
  if (!canBlindRound(ids.length)) return [];
  const all: Pair[] = [];
  for (let i = 0; i < ids.length; i++) for (let j = i + 1; j < ids.length; j++) all.push([ids[i]!, ids[j]!]);
  const order = shuffle(all, rng);
  const picked = ids.length <= BLIND_ROUND_ALL_PAIRS_MAX_TRACKS ? order : order.slice(0, BLIND_ROUND_MAX_PAIRS);
  // Slot assignment is the server's coin; keep (a, b) in a random orientation too.
  return picked.map(([a, b]) => (rng() < 0.5 ? [a, b] : [b, a]));
}

/** Browser RNG for production use. */
export function cryptoRandom(): number {
  const b = new Uint32Array(1);
  crypto.getRandomValues(b);
  return b[0]! / 0x100000000;
}

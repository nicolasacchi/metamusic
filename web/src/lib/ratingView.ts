// The rating block of a take as the caller may see it (U1 C306.4, C313.5). The server shapes it per role: the Owner gets
// `mine`, `others` and `average`; an Editor `mine` and `average`; a Listener `mine` only. Nothing here assumes a field
// exists: `others` and `average` are read only when they are present and well formed, so a Listener's response (no key at
// all) renders exactly like "nobody else rated".
import type { components } from "../api/schema";

export type TrackRating = components["schemas"]["TrackRating"];

export interface Row {
  stars: number | null;
  verdict: string | null;
}

export interface OtherRow extends Row {
  user: string;
  /** True when the server says the user is no longer configured in MM_USERS; the rating still counts in the average. */
  former: boolean;
}

export interface RatingView {
  /** The caller's own rating (null when none). */
  mine: Row | null;
  critic: Row | null;
  /** Other users' ratings; null when the response does not carry them (everyone but the Owner). */
  others: OtherRow[] | null;
  /** Mean over every user's stars; null when the response does not carry it or nobody rated (never shown to a Listener). */
  average: { mean: number; n: number } | null;
}

function row(v: unknown): Row | null {
  if (typeof v !== "object" || v === null) return null;
  const r = v as { stars?: unknown; verdict?: unknown };
  return {
    stars: typeof r.stars === "number" ? r.stars : null,
    verdict: typeof r.verdict === "string" ? r.verdict : null,
  };
}

/** The viewer's own rating: `mine`, or the deprecated `human` alias when an older server sent only that. */
export function mineOf(r: Partial<TrackRating> | null | undefined): Row | null {
  if (!r) return null;
  return row("mine" in r ? r.mine : r.human);
}

export function ratingView(r: Partial<TrackRating> | null | undefined): RatingView {
  if (!r) return { mine: null, critic: null, others: null, average: null };
  let others: OtherRow[] | null = null;
  if (Array.isArray(r.others)) {
    others = [];
    for (const o of r.others as unknown[]) {
      const x = row(o);
      const user = (o as { user?: unknown } | null)?.user;
      if (x && typeof user === "string" && user !== "" && (x.stars !== null || x.verdict !== null)) others.push({ user, ...x, former: (o as { former?: unknown }).former === true });
    }
  }
  let average: RatingView["average"] = null;
  const a: unknown = r.average;
  if (typeof a === "object" && a !== null) {
    const { mean, n } = a as { mean?: unknown; n?: unknown };
    if (typeof mean === "number" && typeof n === "number" && Number.isFinite(mean) && n > 0) average = { mean, n };
  }
  return { mine: mineOf(r), critic: row(r.critic), others, average };
}

/** "4.5 from 3 ratings". */
export function averageLabel(a: { mean: number; n: number }): string {
  return `${Number(a.mean.toFixed(2))} from ${a.n} ${a.n === 1 ? "rating" : "ratings"}`;
}

/** "★4" or "—" for a rater who gave a verdict but no stars. */
export function starsLabel(r: Row): string {
  return r.stars === null ? "no stars" : `★${r.stars}`;
}

/** The Owner's name for a rater: "bob", or "former user (bob)" once bob is no longer configured. */
export function raterLabel(o: { user: string; former: boolean }): string {
  return o.former ? `former user (${o.user})` : o.user;
}

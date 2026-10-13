// Ladder rows and footer (C54.5): rank order exactly as served.
import type { components } from "../api/schema";
import { extentFor, whisker, xFor } from "./ladderScale";

type LadderPage = components["schemas"]["LadderPage"];

export interface LadderRow {
  rank: number;
  trackId: string;
  href: string;
  title: string;
  seed: number;
  u: string;
  record: string;
  provisional: boolean;
  x: number;
  lo: number;
  hi: number;
}

export function ladderRows(page: LadderPage, width: number): { rows: LadderRow[]; extent: number; zeroX: number } {
  const extent = extentFor(page.items);
  return {
    extent,
    zeroX: xFor(0, extent, width),
    rows: page.items.map((it) => {
      const w = whisker(it, extent, width);
      return {
        rank: it.rank,
        trackId: it.track_id,
        href: `/tracks/${encodeURIComponent(it.track_id)}`,
        title: it.title,
        seed: it.seed,
        u: `${it.u >= 0 ? "+" : ""}${it.u.toFixed(2)} ± ${(1.96 * it.sd).toFixed(2)}`,
        record: `${it.wins}-${it.losses}-${it.ties}-${it.both_bad}`,
        provisional: it.provisional,
        ...w,
      };
    }),
  };
}

export interface LadderFooter {
  nu: string;
  iterations: number;
  converged: boolean;
  counts: string;
  slotAWinRate: string | null;
  warning: string | null;
}

export function ladderFooter(page: LadderPage): LadderFooter {
  const d = page.diagnostics;
  const rate = d.slot_a_win_rate;
  const off = rate !== null && d.n_ab >= 20 && (rate < 0.4 - 1e-9 || rate > 0.6 + 1e-9);
  const c = page.counts;
  return {
    nu: page.model.nu.toFixed(2),
    iterations: page.model.iterations,
    converged: page.model.converged,
    counts: `${c.verdicts_human_counted} human verdicts counted, ${c.ties} ties, ${c.both_bad} both bad, ${c.verdicts_critic_excluded} critic and ${c.verdicts_sighted_excluded} sighted excluded`,
    slotAWinRate: rate === null ? null : `${Math.round(rate * 100)}% (n=${d.n_ab})`,
    warning: off && rate !== null ? `Slot A wins ${Math.round(rate * 100)}% of ${d.n_ab} decided duels: the A/B position may be biasing verdicts.` : null,
  };
}

// The energy curve: one polyline per slot on a shared time axis (C131.6). Pure SVG geometry.
import type { components } from "../api/schema";

type Energy = components["schemas"]["Energy"];

export const MAX_POINTS = 120;

/** Thin a series to at most `max` points, keeping the first and last. */
export function downsample<T>(pts: readonly T[], max = MAX_POINTS): T[] {
  if (pts.length <= max || max < 2) return [...pts];
  const out: T[] = [];
  for (let i = 0; i < max; i++) out.push(pts[Math.round((i * (pts.length - 1)) / (max - 1))]!);
  return out;
}

export interface EnergyLine {
  slotId: string;
  path: string;
  startX: number;
  endX: number;
  bpmLabel: string;
  meanY: number | null;
}
export interface EnergyTable {
  slotId: string;
  position: number;
  mean: string;
  bpm: string;
  duration: string;
}
export interface EnergyModel {
  lines: EnergyLine[];
  /** Horizontal guide lines: LUFS value and its y. */
  ticks: { lufs: number; y: number }[];
  table: EnergyTable[];
}

const r1 = (n: number) => Math.round(n * 10) / 10;

export function energyModel(e: Energy, width: number, height: number): EnergyModel {
  const total = e.slots.reduce((a, s) => a + s.duration_s, 0) || e.total_duration_s || 1;
  const all = e.slots.flatMap((s) => s.lufs);
  const table: EnergyTable[] = e.slots.map((s, i) => ({
    slotId: s.slot_id,
    position: i + 1,
    mean: s.mean_lufs === null ? "n/a" : `${r1(s.mean_lufs)} LUFS`,
    bpm: s.bpm === null ? "n/a" : String(Math.round(s.bpm)),
    duration: `${Math.round(s.duration_s)} s`,
  }));
  if (all.length === 0) return { lines: [], ticks: [], table };
  const lo = Math.floor((Math.min(...all, e.target_lufs) - 1) / 2) * 2;
  const hi = Math.ceil((Math.max(...all, e.target_lufs) + 1) / 2) * 2;
  const y = (l: number) => r1(((hi - l) / (hi - lo)) * height);
  const lines: EnergyLine[] = [];
  let t0 = 0;
  for (const s of e.slots) {
    const startX = r1((t0 / total) * width);
    const endX = r1(((t0 + s.duration_s) / total) * width);
    t0 += s.duration_s;
    if (s.lufs.length === 0) continue;
    const pts = downsample(s.lufs);
    const coords = pts.length === 1 ? [`${startX},${y(pts[0]!)}`, `${endX},${y(pts[0]!)}`] : pts.map((l, i) => `${r1(startX + ((endX - startX) * i) / (pts.length - 1))},${y(l)}`);
    lines.push({ slotId: s.slot_id, path: "M" + coords.join("L"), startX, endX, bpmLabel: s.bpm === null ? "" : `${Math.round(s.bpm)} BPM`, meanY: s.mean_lufs === null ? null : y(s.mean_lufs) });
  }
  const ticks: { lufs: number; y: number }[] = [];
  for (let l = hi; l >= lo; l -= 4) ticks.push({ lufs: l, y: y(l) });
  if (ticks.length < 2) ticks.push({ lufs: lo, y: y(lo) });
  return { lines, ticks, table };
}

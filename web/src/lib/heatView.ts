// Pure helpers and the load state of the heat strip (M5-W1, C256, C262).
import type { createApi } from "../api/client";
import { getListenHeat, type ListenHeat, type ListenHotspot } from "./heatApi";
import { guarded, toLoad, type Load } from "./songPrView";
import { fmt } from "./time";

type Api = ReturnType<typeof createApi>;

export async function loadHeat(api: Api, trackId: string): Promise<Load<ListenHeat>> {
  return toLoad(await guarded("Could not load the listening heat", () => getListenHeat(api, trackId)));
}

/** Bin values scaled to 0..1 by the largest bin; all-zero stays zero. */
export function binLevels(v: readonly number[] | null | undefined): number[] {
  if (!v || v.length === 0) return [];
  const max = Math.max(...v);
  return v.map((x) => (max > 0 ? Math.max(0, x) / max : 0));
}

const r3 = (x: number) => Math.round(x * 1000) / 1000;

/** The three bands of the strip, each `bins` long (a short or null array is padded with zeros). */
export function strips(h: ListenHeat): { heard: number[]; hot: number[]; skips: number[] } {
  const n = Math.max(0, h.bins);
  const pad = (v: readonly number[] | null | undefined) => Array.from({ length: n }, (_, i) => v?.[i] ?? 0);
  const rep = pad(h.replays);
  const rew = pad(h.rewinds);
  return {
    heard: binLevels(pad(h.heard)).map(r3),
    hot: binLevels(rep.map((x, i) => x + (rew[i] ?? 0))).map(r3),
    skips: binLevels(pad(h.skips)).map(r3),
  };
}

export function chipText(h: ListenHotspot): string {
  const span = `${fmt(h.from_s)}–${fmt(h.to_s)}`;
  return h.label ? `${span} · ${h.label}` : span;
}

export function heatSummary(h: ListenHeat): string {
  if (h.sessions === 0) return "No listening recorded yet";
  const parts = [`${h.sessions} session${h.sessions === 1 ? "" : "s"}`];
  if (h.completion_rate !== null) parts.push(`${Math.round(h.completion_rate * 100)}% completed`);
  const n = h.hotspots?.length ?? 0;
  parts.push(`${n} hotspot${n === 1 ? "" : "s"}`);
  return `Listening heat: ${parts.join(", ")}`;
}

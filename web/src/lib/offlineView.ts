// Pure view helpers for the /offline page.
import type { LocalItem, OfflineSet, Status, SyncSummary } from "../api/offline";

export interface OfflineRow {
  track_id: string;
  title: string;
  album: string;
  duration_s: number;
  status: Status;
  inSet: boolean;
  local: LocalItem | null;
}

/** Online: the set (in its order) merged with the local state, plus local-only items. Offline (set null): local items only. */
export function mergeRows(set: OfflineSet | null, local: LocalItem[], status: (id: string) => Status): OfflineRow[] {
  const byId = new Map(local.map((l) => [l.track_id, l]));
  const rows: OfflineRow[] = [];
  const seen = new Set<string>();
  if (set) {
    for (const it of [...set.items].sort((a, b) => a.position - b.position)) {
      seen.add(it.track_id);
      rows.push({ track_id: it.track_id, title: it.title, album: it.album, duration_s: it.duration_s, status: status(it.track_id), inSet: true, local: byId.get(it.track_id) ?? null });
    }
  }
  const rest = local.filter((l) => !seen.has(l.track_id)).sort((a, b) => a.title.localeCompare(b.title));
  for (const l of rest) rows.push({ track_id: l.track_id, title: l.title, album: l.album, duration_s: l.duration_s, status: status(l.track_id), inSet: false, local: l });
  return rows;
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

export function syncMessage(s: SyncSummary): string {
  const parts: string[] = [];
  if (s.stopped === "quota") parts.push("Stopped: not enough free storage on this device.");
  else if (s.stopped === "aborted") parts.push("Stopped.");
  const work: string[] = [];
  if (s.downloaded.length) work.push(`Downloaded ${s.downloaded.length}`);
  if (s.evicted.length) work.push(`removed ${s.evicted.length}`);
  if (work.length) parts.push(work.join(", ") + ".");
  if (s.failed.length) parts.push(`${s.failed.length} failed.`);
  return parts.length ? parts.join(" ") : "Already up to date.";
}

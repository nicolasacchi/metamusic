// Reactive offline store over api/offline.ts (names frozen for later milestones, C115). When `available` is
// false every offline UI element is hidden and nothing here touches the network or storage.
import { createOfflineManager, isNetworkError, realDeps, type Format, type OfflineDeps, type LocalItem, type LocalSource, type OfflineManager, type OfflineSet, type Status, type SyncSummary } from "../api/offline";
import { detectOfflineAvailable } from "../api/swRegister";

export type { LocalItem, LocalSource, Status, SyncSummary };

export interface Progress {
  done: number;
  total: number;
}

export interface OfflineEnv {
  available: boolean;
  /** The device's effective format (M1 C22.1); installed by the player module, which owns the device hooks. */
  deviceFormat(): Format;
  manager(deps: OfflineDeps): OfflineManager;
  deps(deviceFormat: () => Format): OfflineDeps;
  storage?: { estimate?(): Promise<{ quota?: number; usage?: number }>; persisted?(): Promise<boolean> } | null;
}

export class OfflineStore {
  available: boolean;
  items = $state<LocalItem[]>([]);
  /** Last known offline set (null until fetched). */
  set = $state<OfflineSet | null>(null);
  progress = $state<Record<string, Progress>>({});
  quota = $state<{ quota: number; usage: number } | null>(null);
  persisted = $state<boolean | null>(null);
  syncing = $state(false);
  lastSummary = $state<SyncSummary | null>(null);
  error = $state("");

  private env: OfflineEnv;
  private mgr: OfflineManager | null = null;
  private depsCache: OfflineDeps | null = null;
  private format: () => Format;
  private statuses = $state<Record<string, Status>>({});
  /* eslint-disable svelte/prefer-svelte-reactivity -- bookkeeping, nothing renders from it */
  private suspect = new Set<string>();
  /* eslint-enable svelte/prefer-svelte-reactivity */

  constructor(env: OfflineEnv) {
    this.env = env;
    this.available = env.available;
    this.format = () => env.deviceFormat();
  }

  /** Installs the device-format source (the player module calls this once). */
  setDeviceFormat(f: () => Format) {
    this.format = f;
  }

  private deps(): OfflineDeps {
    return (this.depsCache ??= this.env.deps(() => this.format()));
  }

  private m(): OfflineManager {
    return (this.mgr ??= this.env.manager(this.deps()));
  }

  /** Re-reads the local stores (items, statuses, storage estimate). */
  async refresh(): Promise<void> {
    if (!this.available) return;
    try {
      const m = this.m();
      this.items = await m.list();
      const next: Record<string, Status> = {};
      for (const i of this.items) next[i.track_id] = "complete";
      this.statuses = next;
      this.persisted = m.persisted ?? (await this.env.storage?.persisted?.().catch(() => null)) ?? null;
      const est = await this.env.storage?.estimate?.().catch(() => undefined);
      this.quota = est && est.quota !== undefined ? { quota: est.quota, usage: est.usage ?? 0 } : null;
    } catch {
      this.items = [];
    }
  }

  /** get_offline_set; keeps the last value when the network is down. Returns null on failure. */
  async refreshSet(): Promise<OfflineSet | null> {
    if (!this.available) return null;
    try {
      this.set = await this.deps().offlineSet();
      this.error = "";
      return this.set;
    } catch (e) {
      this.error = isNetworkError(e) ? "offline" : "Could not load the offline set";
      return null;
    }
  }

  status(trackId: string): Status {
    return this.statuses[trackId] ?? "none";
  }

  /** "downloaded/total" for the nav badge; null while the set is unknown or empty. */
  get badge(): string | null {
    if (!this.available || !this.set || this.set.items.length === 0) return null;
    const have = this.set.items.filter((i) => this.status(i.track_id) === "complete").length;
    return `${have}/${this.set.items.length}`;
  }

  async download(trackId: string, signal?: AbortSignal): Promise<void> {
    if (!this.available) return;
    this.suspect.delete(trackId);
    let it = this.set?.items.find((i) => i.track_id === trackId);
    if (!it) it = (await this.refreshSet())?.items.find((i) => i.track_id === trackId);
    this.statuses = { ...this.statuses, [trackId]: "partial" };
    this.progress = { ...this.progress, [trackId]: { done: 0, total: 0 } };
    try {
      await this.m().downloadById(
        trackId,
        { title: it?.title ?? trackId, album: it?.album ?? "", duration_s: it?.duration_s ?? 0 },
        (done, total) => (this.progress = { ...this.progress, [trackId]: { done, total } }),
        signal,
      );
    } finally {
      const { [trackId]: _gone, ...rest } = this.progress;
      void _gone;
      this.progress = rest;
      await this.refresh();
    }
  }

  async remove(trackId: string): Promise<void> {
    if (!this.available) return;
    await this.m().evict(trackId);
    await this.refresh();
  }

  async sync(signal?: AbortSignal): Promise<SyncSummary> {
    this.syncing = true;
    this.error = "";
    try {
      const summary = await this.m().sync((id, done, total) => (this.progress = { ...this.progress, [id]: { done, total } }), signal);
      this.lastSummary = summary;
      return summary;
    } finally {
      this.syncing = false;
      this.progress = {};
      await this.refresh();
      await this.refreshSet();
    }
  }

  /** A complete local file for the track, or null (also null once the player marked it suspect). */
  async localSource(trackId: string): Promise<(LocalSource & { title?: string }) | null> {
    if (!this.available || this.suspect.has(trackId)) return null;
    try {
      return await this.m().localSource(trackId);
    } catch {
      return null;
    }
  }

  /** Hooks for the player (C115.4). */
  readonly hooks = {
    source: (trackId: string) => this.localSource(trackId),
    meta: async (trackId: string) => this.items.find((i) => i.track_id === trackId) ?? (await this.loadItem(trackId)),
    markSuspect: (trackId: string) => void this.suspect.add(trackId),
  };

  private async loadItem(trackId: string): Promise<{ title: string; album: string } | null> {
    if (!this.available) return null;
    try {
      const i = (await this.m().list()).find((x) => x.track_id === trackId);
      return i ? { title: i.title, album: i.album } : null;
    } catch {
      return null;
    }
  }
}

export const offline = new OfflineStore({
  available: detectOfflineAvailable(),
  deviceFormat: () => "flac",
  manager: createOfflineManager,
  deps: realDeps,
  storage: typeof navigator !== "undefined" ? navigator.storage : null,
});

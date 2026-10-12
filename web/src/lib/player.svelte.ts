// Global player store (C22.1). The single audio element lives in App.svelte's footer and is
// handed over by attach(); it is never re-created on navigation.
import { api } from "../api/client";
import { session } from "../api/session";
import type { components } from "../api/schema";

export type MediaLinks = components["schemas"]["MediaLinksOutBody"];
export type Format = "flac" | "mp3";
export type Quality = "lossless" | "high" | "auto";

export interface Connection {
  saveData?: boolean;
  type?: string;
}

/** Device effective format: lossless -> flac, high -> mp3, auto -> mp3 on data-saver or cellular. */
export function formatFor(quality: Quality | string, conn?: Connection): Format {
  if (quality === "high") return "mp3";
  if (quality === "auto") return conn?.saveData || conn?.type === "cellular" ? "mp3" : "flac";
  return "flac";
}

/** What the player needs from the device layer (C22.3, owned by device.svelte.ts). */
export interface DeviceHooks {
  quality(): Quality | string;
  connection(): Connection | undefined;
  /** FLAC failed to decode/play on this device: persist a failed probe (quality -> high unless user-chosen). */
  recordFailedProbe(): void;
  /** Called synchronously inside the user gesture of play()/toggle() (starts the probe, never delays playback). */
  onGesture?(): void;
}

const QUALITY_KEY = "mm:quality";
const QUALITY_SOURCE_KEY = "mm:quality-source";

function ls(): Storage | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

/** Fallback used until device.svelte.ts installs its own hooks via configure(). */
export const localDevice: DeviceHooks = {
  quality: () => ls()?.getItem(QUALITY_KEY) ?? "lossless",
  connection: () =>
    (navigator as Navigator & { connection?: Connection }).connection,
  recordFailedProbe() {
    const s = ls();
    if (!s || s.getItem(QUALITY_SOURCE_KEY) === "user") return;
    s.setItem(QUALITY_KEY, "high");
    s.setItem(QUALITY_SOURCE_KEY, "probe");
  },
};

export interface PlayerDeps {
  fetchLinks(trackId: string): Promise<MediaLinks | null>;
  device: DeviceHooks;
  now(): number;
}

export interface PlayOptions {
  title?: string;
  album?: string;
  /** Seconds; applied in `loadedmetadata` (iOS ignores earlier writes). */
  startAt?: number;
  /** false: load paused (deep link). Default true. */
  autoplay?: boolean;
}

interface CacheEntry {
  links: MediaLinks;
  validUntil: number;
}

const MEDIA_ERR_NETWORK = 2;
const MEDIA_ERR_DECODE = 3;
const MEDIA_ERR_SRC_NOT_SUPPORTED = 4;
const MARGIN_MS = 10 * 60_000;

export class Player {
  el = $state<HTMLAudioElement | null>(null);
  title = $state("Nothing playing");
  album = $state("");
  src = $state("");
  state = $state({
    trackId: "",
    queue: [] as string[],
    index: -1,
    playing: false,
    current: 0,
    duration: 0,
    format: "flac" as Format,
    error: "",
  });

  /* Non-reactive bookkeeping: nothing renders from these collections. */
  /* eslint-disable svelte/prefer-svelte-reactivity */
  private deps: PlayerDeps;
  private cache = new Map<string, CacheEntry>();
  private inflight = new Map<string, Promise<MediaLinks | null>>();
  private pendingSeek: number | null = null;
  private autoplay = true;
  private reminted = new Set<string>();
  private forceMp3 = new Set<string>();
  /* eslint-enable svelte/prefer-svelte-reactivity */
  private cleanup: (() => void) | null = null;
  /** Bumped on every track change so a slow async link fetch never clobbers a newer play. */
  private epoch = 0;

  constructor(deps: PlayerDeps) {
    this.deps = deps;
  }

  configure(d: Partial<PlayerDeps>) {
    this.deps = { ...this.deps, ...d };
  }

  attach(el: HTMLAudioElement | null) {
    this.cleanup?.();
    this.cleanup = null;
    this.el = el;
    if (!el) return;
    const on = (type: string, fn: () => void) => {
      el.addEventListener(type, fn);
      return () => el.removeEventListener(type, fn);
    };
    const offs = [
      on("loadedmetadata", () => this.onLoadedMetadata()),
      on("durationchange", () => this.syncDuration()),
      on("timeupdate", () => (this.state.current = el.currentTime || 0)),
      on("seeked", () => (this.state.current = el.currentTime || 0)),
      on("play", () => (this.state.playing = true)),
      on("playing", () => {
        this.state.playing = true;
        this.state.error = "";
        this.prefetchNext();
      }),
      on("pause", () => (this.state.playing = false)),
      on("ended", () => this.onEnded()),
      on("error", () => this.onError()),
    ];
    try {
      const as = (navigator as Navigator & { audioSession?: { type: string } }).audioSession;
      if (as) as.type = "playback";
    } catch {
      /* optional API */
    }
    this.cleanup = () => offs.forEach((f) => f());
  }

  /** Start playing a track (optionally with a queue it belongs to). Must be called from a user gesture. */
  play(trackId: string, queue?: string[], opts: PlayOptions = {}) {
    this.deps.device.onGesture?.();
    this.start(trackId, queue, opts);
  }

  /** Load a track paused; if it is already current, just seek. */
  load(trackId: string, queue?: string[], opts: PlayOptions = {}) {
    if (this.state.trackId === trackId && this.el?.src) {
      if (opts.startAt != null) this.seek(opts.startAt);
      return;
    }
    this.start(trackId, queue, { ...opts, autoplay: false });
  }

  toggle() {
    const el = this.el;
    if (!el || !this.state.trackId) return;
    if (el.paused) {
      this.deps.device.onGesture?.();
      void el.play()?.catch?.((e: unknown) => this.fail(e));
    } else el.pause();
  }

  seek(s: number) {
    const el = this.el;
    if (!el || !Number.isFinite(s)) return;
    const max = Number.isFinite(el.duration) && el.duration > 0 ? el.duration : Infinity;
    const t = Math.min(Math.max(0, s), max);
    el.currentTime = t;
    this.state.current = t;
  }

  next() {
    const s = this.state;
    if (s.index + 1 < s.queue.length) this.goto(s.index + 1);
  }

  prev() {
    const s = this.state;
    if (s.current > 3 || s.index <= 0) this.seek(0);
    else this.goto(s.index - 1);
  }

  setMeta(title: string, album = "") {
    this.title = title;
    this.album = album;
  }

  private goto(index: number) {
    const id = this.state.queue[index];
    if (id === undefined) return;
    this.start(id, this.state.queue, { autoplay: true, title: "" });
  }

  private start(trackId: string, queue: string[] | undefined, opts: PlayOptions) {
    const s = this.state;
    this.epoch++;
    const q = queue && queue.includes(trackId) ? [...queue] : queue ? [trackId] : s.queue.includes(trackId) ? s.queue : [trackId];
    s.queue = q;
    s.index = q.indexOf(trackId);
    s.trackId = trackId;
    s.error = "";
    s.current = opts.startAt ?? 0;
    s.duration = 0;
    this.pendingSeek = opts.startAt != null && opts.startAt > 0 ? opts.startAt : null;
    this.autoplay = opts.autoplay !== false;
    this.reminted.delete(trackId);
    if (opts.title !== undefined) this.title = opts.title || this.title;
    if (opts.album !== undefined && opts.album) this.album = opts.album;
    this.begin(trackId);
  }

  /** Synchronous when links are cached (required for `ended` and gesture-bound play()). */
  private begin(trackId: string) {
    const cached = this.cached(trackId);
    if (cached) {
      this.apply(cached);
      return;
    }
    const epoch = this.epoch;
    void this.links(trackId).then((l) => {
      if (epoch !== this.epoch) return;
      if (l) this.apply(l);
      else this.state.error = "Could not load media links";
    });
  }

  private apply(links: MediaLinks) {
    const el = this.el;
    if (!el) return;
    const format = this.formatFor(links.track_id);
    this.state.format = format;
    const url = format === "flac" ? links.flac.url : links.mp3.url;
    this.src = url;
    el.src = url;
    if (this.autoplay) void el.play()?.catch?.((e: unknown) => this.fail(e));
  }

  private formatFor(trackId: string): Format {
    if (this.forceMp3.has(trackId)) return "mp3";
    return formatFor(this.deps.device.quality(), this.deps.device.connection());
  }

  private fail(e: unknown) {
    // Autoplay rejections are routine (user gesture lost); media errors arrive via `error`.
    if (e instanceof DOMException && (e.name === "AbortError" || e.name === "NotAllowedError")) {
      this.state.playing = false;
      return;
    }
    this.state.error = "Playback failed";
  }

  private cached(trackId: string): MediaLinks | null {
    const c = this.cache.get(trackId);
    if (c && c.validUntil > this.deps.now()) return c.links;
    this.cache.delete(trackId);
    return null;
  }

  private links(trackId: string, force = false): Promise<MediaLinks | null> {
    if (!force) {
      const c = this.cached(trackId);
      if (c) return Promise.resolve(c);
      const f = this.inflight.get(trackId);
      if (f) return f;
    }
    const p = this.deps
      .fetchLinks(trackId)
      .then((l) => {
        if (l) {
          const exp = Date.parse(l.expires_at);
          const margin = Math.min(MARGIN_MS, (l.ttl_s * 1000) / 2);
          this.cache.set(trackId, { links: l, validUntil: exp - margin });
        }
        return l;
      })
      .catch(() => null)
      .finally(() => {
        this.inflight.delete(trackId);
      });
    this.inflight.set(trackId, p);
    return p;
  }

  private prefetchNext() {
    const s = this.state;
    const id = s.queue[s.index + 1];
    if (id !== undefined) void this.links(id);
  }

  private syncDuration() {
    const d = this.el?.duration;
    this.state.duration = d !== undefined && Number.isFinite(d) ? d : 0;
  }

  private onLoadedMetadata() {
    this.syncDuration();
    const el = this.el;
    if (el && this.pendingSeek != null) {
      const t = this.pendingSeek;
      this.pendingSeek = null;
      this.seek(t);
    }
  }

  private onEnded() {
    this.state.playing = false;
    const s = this.state;
    const nextId = s.queue[s.index + 1];
    if (nextId === undefined) return;
    this.epoch++;
    s.index += 1;
    s.trackId = nextId;
    s.current = 0;
    s.duration = 0;
    this.pendingSeek = null;
    this.autoplay = true;
    this.reminted.delete(nextId);
    this.begin(nextId);
  }

  private onError() {
    const el = this.el;
    const code = el?.error?.code;
    const id = this.state.trackId;
    if (!el || !id) return;
    const at = el.currentTime || this.state.current || 0;
    if (code === MEDIA_ERR_NETWORK) {
      if (this.reminted.has(id)) {
        this.state.error = "Network error while loading audio";
        return;
      }
      this.reminted.add(id);
      this.cache.delete(id);
      const epoch = this.epoch;
      void this.links(id, true).then((l) => {
        if (epoch !== this.epoch) return;
        if (!l) {
          this.state.error = "Could not refresh media links";
          return;
        }
        this.pendingSeek = at > 0 ? at : null;
        this.autoplay = true;
        this.apply(l);
      });
    } else if (
      (code === MEDIA_ERR_SRC_NOT_SUPPORTED || code === MEDIA_ERR_DECODE) &&
      this.state.format === "flac"
    ) {
      this.forceMp3.add(id);
      this.deps.device.recordFailedProbe();
      const links = this.cached(id);
      this.pendingSeek = at > 0 ? at : null;
      this.autoplay = true;
      if (links) this.apply(links);
      else {
        const epoch = this.epoch;
        void this.links(id).then((l) => {
          if (epoch === this.epoch && l) this.apply(l);
        });
      }
    } else {
      this.state.error = "Playback error";
    }
  }
}

export const player = new Player({
  fetchLinks: async (id) => {
    const r = await api.GET("/api/tracks/{id}/media", { params: { path: { id } } });
    return r.data ?? null;
  },
  device: localDevice,
  now: () => Date.now(),
});

session.configure({ isPlaying: () => player.state.playing });

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

/** In-memory fallback until device.svelte.ts installs its own hooks via configure() (C22.3 owns persistence). */
export function memoryDevice(): DeviceHooks {
  let quality: Quality | string = "lossless";
  let source = "";
  return {
    quality: () => quality,
    connection: () => (navigator as Navigator & { connection?: Connection }).connection,
    recordFailedProbe() {
      if (source === "user") return;
      quality = "high";
      source = "probe";
    },
  };
}

export const localDevice: DeviceHooks = memoryDevice();

export interface PlayerDeps {
  fetchLinks(trackId: string): Promise<MediaLinks | null>;
  device: DeviceHooks;
  now(): number;
  /** Resolves display metadata for a track (title/album); optional. */
  fetchMeta?(trackId: string): Promise<{ title: string; album: string } | null>;
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
// Contract: cache until expires_at - 10 min. Deviation: never more than half the TTL, so short-TTL
// links (agents, tests) are not considered expired the moment they are minted. Equal for the 12 h human TTL.
const MARGIN_MS = 10 * 60_000;
const LOADING_TITLE = "Loading…";
const FALLBACK_TITLE = "Untitled track";

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
    /** The current track has no lossless file (lossy reference): it plays its MP3 master, whatever the device quality. */
    noFlac: false,
    error: "",
  });

  /* Non-reactive bookkeeping: nothing renders from these collections. */
  /* eslint-disable svelte/prefer-svelte-reactivity */
  private deps: PlayerDeps;
  private cache = new Map<string, CacheEntry>();
  private inflight = new Map<string, Promise<MediaLinks | null>>();
  private pendingSeek: number | null = null;
  private autoplay = true;
  /** Playback intent: cleared only by a user pause, never by the element's own `pause` event (Chromium pauses before `error`). */
  private wantPlay = false;
  private reminted = new Set<string>();
  private forceMp3 = new Set<string>();
  private meta = new Map<string, { title: string; album: string }>();
  private metaInflight = new Map<string, Promise<{ title: string; album: string } | null>>();
  private probePending = new Set<string>();
  /* eslint-enable svelte/prefer-svelte-reactivity */
  private cleanup: (() => void) | null = null;
  /** expires_at (ms) of the link currently applied to the element. */
  private appliedExp = 0;
  /** Track whose links were last applied to the element: only its metadata may consume a pending seek. */
  private appliedTrack = "";
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
        this.reminted.delete(this.state.trackId);
        if (!this.meta.has(this.state.trackId) && this.title === FALLBACK_TITLE) this.syncMeta(this.state.trackId);
        this.prefetchNext();
      }),
      on("pause", () => (this.state.playing = false)),
      // The media load algorithm (src change, load()) pauses the element without a `pause` event.
      on("emptied", () => (this.state.playing = !el.paused)),
      on("abort", () => (this.state.playing = !el.paused)),
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
    if (this.state.trackId === trackId && (this.el?.src || this.inflight.has(trackId))) {
      if (opts.startAt != null) {
        // iOS ignores currentTime writes before metadata is known.
        if (!this.el || this.el.readyState < 1 || !this.el.src) this.pendingSeek = opts.startAt;
        else this.seek(opts.startAt);
      }
      return;
    }
    this.start(trackId, queue, { ...opts, autoplay: false });
  }

  toggle() {
    const el = this.el;
    if (!el || !this.state.trackId) return;
    if (el.paused) this.resume();
    else this.pause();
  }

  /** User pause: the only place playback intent is cleared. */
  pause() {
    this.wantPlay = false;
    this.autoplay = false;
    this.el?.pause();
  }

  /** User resume (toggle, Media Session 'play'). */
  resume() {
    const el = this.el;
    if (!el || !this.state.trackId) return;
    this.deps.device.onGesture?.();
    this.wantPlay = true;
    this.autoplay = true;
    if (el.error || (el.readyState === 0 && !this.cached(this.state.trackId))) {
      // Errored element (failed FLAC and MP3), or nothing was ever fetched (preload=none) and the
      // links expired meanwhile: apply/mint a source again instead of play()ing a dead element.
      const at = this.state.current;
      this.pendingSeek ??= at > 0 ? at : null;
      this.reminted.delete(this.state.trackId);
      this.epoch++;
      this.begin(this.state.trackId);
    } else void el.play()?.catch?.((e: unknown) => this.fail(e));
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
    if (this.state.trackId) this.meta.set(this.state.trackId, { title, album });
    this.title = title;
    this.album = album;
    this.announce();
  }

  private announce() {
    try {
      this.el?.dispatchEvent(new Event("mm:meta"));
    } catch {
      /* no DOM */
    }
  }

  /** Make title/album follow the current track: cached value now, else clear the stale one and fetch. */
  private syncMeta(trackId: string, hint?: string) {
    const m = this.meta.get(trackId);
    if (m) {
      this.title = m.title;
      this.album = m.album;
      this.announce();
      return;
    }
    this.title = hint ?? LOADING_TITLE;
    this.album = "";
    this.announce();
    void this.loadMeta(trackId).then((r) => {
      if (this.state.trackId !== trackId) return;
      this.title = hint ?? (r ? r.title : FALLBACK_TITLE);
      this.album = r ? r.album : "";
      this.announce();
    });
  }

  private async loadMeta(trackId: string): Promise<{ title: string; album: string } | null> {
    const m = this.meta.get(trackId);
    if (m) return m;
    if (!this.deps.fetchMeta) return null;
    const f = this.metaInflight.get(trackId);
    if (f) return f;
    const p = (async () => {
      try {
        const r = await this.deps.fetchMeta!(trackId);
        if (r) this.meta.set(trackId, r);
        return r;
      } catch {
        return null;
      }
    })().finally(() => {
      this.metaInflight.delete(trackId);
    });
    this.metaInflight.set(trackId, p);
    return p;
  }

  private goto(index: number) {
    const id = this.state.queue[index];
    if (id === undefined) return;
    this.start(id, this.state.queue, { autoplay: true });
  }

  private start(trackId: string, queue: string[] | undefined, opts: PlayOptions) {
    const s = this.state;
    this.epoch++;
    const q = queue && queue.includes(trackId) ? [...queue] : queue ? [trackId] : s.queue.includes(trackId) ? s.queue : [trackId];
    s.queue = q;
    s.index = q.indexOf(trackId);
    s.trackId = trackId;
    s.noFlac = false;
    s.error = "";
    s.current = opts.startAt ?? 0;
    s.duration = 0;
    this.pendingSeek = opts.startAt != null && opts.startAt > 0 ? opts.startAt : null;
    this.autoplay = opts.autoplay !== false;
    this.wantPlay = this.autoplay;
    this.probePending.delete(trackId);
    this.reminted.delete(trackId);
    // Cache only a complete pair; a title alone is shown at once while the album still resolves.
    if (opts.title && opts.album !== undefined) this.meta.set(trackId, { title: opts.title, album: opts.album });
    this.syncMeta(trackId, opts.title);
    this.begin(trackId);
  }

  /** Synchronous when links are cached (required for `ended` and gesture-bound play()). */
  private begin(trackId: string) {
    const cached = this.cached(trackId);
    if (cached) {
      this.apply(cached);
      return;
    }
    // Unlock the element inside the user gesture (iOS/WebKit) before the async fetch.
    if (this.autoplay && this.el?.load) {
      this.el.load();
      this.state.playing = !this.el.paused;
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
    // A track without a lossless file (flac === null: a lossy reference) always plays its MP3 link.
    const format = links.flac ? this.formatFor(links.track_id) : "mp3";
    this.state.format = format;
    this.state.noFlac = !links.flac;
    const url = format === "flac" && links.flac ? links.flac.url : links.mp3.url;
    this.appliedExp = Date.parse(links.expires_at);
    this.src = url;
    this.appliedTrack = links.track_id;
    el.src = url;
    this.state.playing = !el.paused;
    if (this.autoplay) {
      this.wantPlay = true;
      void el.play()?.catch?.((e: unknown) => this.fail(e));
    }
  }

  private formatFor(trackId: string): Format {
    if (this.forceMp3.has(trackId)) return "mp3";
    return formatFor(this.deps.device.quality(), this.deps.device.connection());
  }

  private fail(e: unknown) {
    // Autoplay rejections are routine (user gesture lost); media errors arrive via `error`.
    if (e instanceof DOMException && e.name === "NotAllowedError") {
      this.wantPlay = false;
      this.state.playing = false;
      this.state.error = "Tap play to start";
      return;
    }
    if (e instanceof DOMException && e.name === "AbortError") {
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
    if (id !== undefined) {
      void this.links(id);
      void this.loadMeta(id);
    }
  }

  private syncDuration() {
    const d = this.el?.duration;
    this.state.duration = d !== undefined && Number.isFinite(d) ? d : 0;
  }

  private onLoadedMetadata() {
    this.syncDuration();
    const id = this.state.trackId;
    // The MP3 loaded after a FLAC failure: server and network work, so the FLAC failure was a codec failure.
    if (this.probePending.delete(id) && this.state.format === "mp3") this.deps.device.recordFailedProbe();
    const el = this.el;
    // The unlock load() in begin() re-reads the previous track's source; its metadata is not ours.
    if (el && this.pendingSeek != null && this.appliedTrack === id) {
      const t = this.pendingSeek;
      this.pendingSeek = null;
      this.seek(t);
    }
  }

  private onEnded() {
    this.state.playing = false;
    const s = this.state;
    const nextId = s.queue[s.index + 1];
    if (nextId === undefined) {
      this.wantPlay = false;
      return;
    }
    this.epoch++;
    s.index += 1;
    s.trackId = nextId;
    s.noFlac = false;
    s.current = 0;
    s.duration = 0;
    this.pendingSeek = null;
    this.autoplay = true;
    this.wantPlay = true;
    this.probePending.delete(nextId);
    this.reminted.delete(nextId);
    this.syncMeta(nextId);
    this.begin(nextId);
  }

  private onError() {
    const el = this.el;
    const code = el?.error?.code;
    const id = this.state.trackId;
    if (!el || !id) return;
    // Firefox reports a failing audio output (no device, e.g. headless CI) as MEDIA_ERR_DECODE with this message
    // after decoding already worked, and keeps the media clock running. It says nothing about the codec: do not
    // fall back to MP3, record a failed probe, or touch playback state.
    if (/MediaSink/i.test(el.error?.message ?? "")) return;
    const at = el.currentTime || this.state.current || 0;
    const resume = this.wantPlay;
    // Chrome/Firefox report a 403 on the media fetch as code 4 (not 2): an expired link is a re-mint, not a codec problem.
    const expired = this.appliedExp > 0 && this.deps.now() >= this.appliedExp;
    const remint =
      code === MEDIA_ERR_NETWORK ||
      ((code === MEDIA_ERR_SRC_NOT_SUPPORTED || code === MEDIA_ERR_DECODE) && expired && !this.reminted.has(id));
    if (remint) {
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
        this.autoplay = resume;
        this.apply(l);
      });
    } else if (
      (code === MEDIA_ERR_SRC_NOT_SUPPORTED || code === MEDIA_ERR_DECODE) &&
      this.state.format === "flac"
    ) {
      // Code 3/4 is also what browsers report for HTTP/network failures on first load, so the failed
      // probe is recorded only once the MP3 reaches loadedmetadata (see onLoadedMetadata).
      this.forceMp3.add(id);
      this.probePending.add(id);
      const links = this.cached(id);
      this.pendingSeek = at > 0 ? at : null;
      this.autoplay = resume;
      if (links) this.apply(links);
      else {
        const epoch = this.epoch;
        void this.links(id).then((l) => {
          if (epoch === this.epoch && l) this.apply(l);
        });
      }
    } else {
      // An MP3 that also fails proves nothing about the codec: record nothing, stop forcing MP3.
      this.probePending.delete(id);
      this.forceMp3.delete(id);
      this.state.error = "Playback error";
    }
  }
}

export interface TrackRef {
  title: string;
  rootId: string | null;
}

/** Display metadata: album is the root track's title (C22.2). */
export async function resolveMeta(
  id: string,
  get: (id: string) => Promise<TrackRef | null>,
): Promise<{ title: string; album: string } | null> {
  const t = await get(id);
  if (!t) return null;
  if (!t.rootId || t.rootId === id) return { title: t.title, album: t.title };
  const root = await get(t.rootId).catch(() => null);
  return { title: t.title, album: root?.title ?? "" };
}

async function getTrackRef(id: string): Promise<TrackRef | null> {
  const r = await api.GET("/api/tracks/{id}", { params: { path: { id } } });
  return r.data ? { title: r.data.title, rootId: r.data.lineage?.root_track_id ?? null } : null;
}

export const player = new Player({
  fetchLinks: async (id) => {
    const r = await api.GET("/api/tracks/{id}/media", { params: { path: { id } } });
    return r.data ?? null;
  },
  device: localDevice,
  now: () => Date.now(),
  fetchMeta: (id) => resolveMeta(id, getTrackRef),
});

session.configure({ isPlaying: () => player.state.playing });

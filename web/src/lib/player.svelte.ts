// Global player store (C22.1). The single audio element lives in App.svelte's footer and is
// handed over by attach(); it is never re-created on navigation.
import { api } from "../api/client";
import { session } from "../api/session";
import { offline } from "./offline.svelte";
import type { LocalSource } from "../api/offline";
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

/** What the player needs from the offline store (C115.4, owned by offline.svelte.ts). */
export interface LocalHooks {
  /** A complete local file for the track, or null. */
  source(trackId: string): Promise<LocalSource | null>;
  /** Title/album from the local stores (no API call). */
  meta(trackId: string): Promise<{ title: string; album: string } | null>;
  /** The local file failed to play: stop offering it until it is downloaded again. */
  markSuspect(trackId: string): void;
}

export interface PlayerDeps {
  /** Offline source selection; absent: M1's signed-link path only. */
  local?: LocalHooks;
  /** navigator.serviceWorker.controller !== null: only then may /media/<sha>.<ext> be served locally. */
  hasController?(): boolean;
  /** navigator.onLine; offline, metadata comes from the local store without an API call. */
  online?(): boolean;
  fetchLinks(trackId: string): Promise<MediaLinks | null>;
  device: DeviceHooks;
  now(): number;
  /** Resolves display metadata for a track (title/album); optional. */
  fetchMeta?(trackId: string): Promise<{ title: string; album: string } | null>;
  /** Warms the first bytes of a signed media URL (the next track) so the swap at `ended` starts quickly. */
  warm?(url: string): void;
}

export interface PlayOptions {
  title?: string;
  album?: string;
  /** Seconds; applied in `loadedmetadata` (iOS ignores earlier writes). */
  startAt?: number;
  /** false: load paused (deep link). Default true. */
  autoplay?: boolean;
  /** Fetches more queue entries (the next list page); asked when the queue is about to run out. */
  more?: () => Promise<string[]>;
}

/** A playback diagnostic; the listen recorder turns it into an outbox event (kinds autoplay_blocked|stall_end|advance|error). */
export interface PlayerDiag {
  kind: "autoplay_blocked" | "stall_end" | "advance" | "error";
  detail?: string;
  track_id: string;
  pos_ms: number;
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
/** Watchdog: `ended` did not fire although playback sits at the end (Android Firefox, range-served FLAC). */
const END_EPS_S = 0.3;
const END_STALL_MS = 1500;
const WATCHDOG_MS = 500;
const STALL_REPORT_MS = 500;
const PREFETCH_AT = 0.8;
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
    /** play() was refused (autoplay policy): the queue position is kept and the next interaction resumes. */
    pendingPlay: false,
    /** The queue played out: nothing is next (visible state, Media Session `none`). */
    endOfQueue: false,
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
  /** Local-file lookups (null: not downloaded); refreshed by prefetchNext so a track change can apply one synchronously. */
  private localInfo = new Map<string, LocalSource | null>();
  /** Local files that failed to play on this page life: never offered again. */
  private noLocal = new Set<string>();
  private meta = new Map<string, { title: string; album: string }>();
  private metaInflight = new Map<string, Promise<{ title: string; album: string } | null>>();
  private probePending = new Set<string>();
  /* eslint-enable svelte/prefer-svelte-reactivity */
  private cleanup: (() => void) | null = null;
  /** expires_at (ms) of the link currently applied to the element. */
  private appliedExp = 0;
  /** Track whose source on the element is its local file (a media error then re-mints a signed link). */
  private playingLocal = "";
  /** Track whose links were last applied to the element: only its metadata may consume a pending seek. */
  private appliedTrack = "";
  /** Bumped on every track change so a slow async link fetch never clobbers a newer play. */
  private epoch = 0;
  /** The current play already advanced/ended: `ended`, the watchdog and a late event act once. */
  private finished = false;
  private lastPos = 0;
  private lastProgressAt = 0;
  private stallStart: number | null = null;
  private prefetchedAt = -1;
  private timer: ReturnType<typeof setInterval> | null = null;
  private playCause = "user";
  /** Explicit "play next" entries sitting right after the current track. */
  private queuedNext = 0;
  private source: (() => Promise<string[]>) | null = null;
  private sourceBusy = false;
  private sourceDone = false;
  private stopActivation: (() => void) | null = null;

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
      on("timeupdate", () => this.onTimeUpdate()),
      on("seeked", () => {
        this.state.current = el.currentTime || 0;
        this.finished = false;
      }),
      on("play", () => this.onPlay()),
      on("waiting", () => {
        if (this.wantPlay && this.stallStart === null) this.stallStart = this.deps.now();
      }),
      on("playing", () => {
        this.endStall();
        this.state.playing = true;
        this.state.pendingPlay = false;
        this.state.error = "";
        this.reminted.delete(this.state.trackId);
        if (!this.meta.has(this.state.trackId) && this.title === FALLBACK_TITLE) this.syncMeta(this.state.trackId);
        this.prefetchNext();
      }),
      on("pause", () => {
        this.state.playing = false;
        this.stallStart = null;
      }),
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
    this.timer = setInterval(() => this.tick(), WATCHDOG_MS);
    (this.timer as { unref?: () => void }).unref?.();
    this.cleanup = () => {
      offs.forEach((f) => f());
      this.stopActivation?.();
      this.stopActivation = null;
      if (this.timer) clearInterval(this.timer);
      this.timer = null;
    };
  }

  /**
   * Resume a refused play() on the next user interaction or when the page becomes visible again
   * (Firefox Android may refuse play() after the screen was off). Returns a cleanup.
   */
  watchActivation(doc: Pick<Document, "addEventListener" | "removeEventListener" | "visibilityState">): () => void {
    this.stopActivation?.();
    const act = () => {
      if (this.state.pendingPlay) this.resume();
    };
    const vis = () => {
      if (doc.visibilityState !== "visible") return;
      this.tick();
      act();
    };
    const types = ["click", "keydown", "touchend"];
    for (const t of types) doc.addEventListener(t, act, true);
    doc.addEventListener("visibilitychange", vis);
    const stop = () => {
      for (const t of types) doc.removeEventListener(t, act, true);
      doc.removeEventListener("visibilitychange", vis);
    };
    this.stopActivation = stop;
    return () => {
      stop();
      if (this.stopActivation === stop) this.stopActivation = null;
    };
  }

  /** Playback intent (wantPlay), for the Media Session playbackState. */
  get playIntent(): boolean {
    return this.wantPlay;
  }

  get hasNext(): boolean {
    return this.state.index + 1 < this.state.queue.length;
  }

  /** Insert a track right after the current one (after earlier "play next" entries). */
  playNext(trackId: string) {
    const s = this.state;
    if (!s.trackId) {
      this.load(trackId, [trackId]);
      return;
    }
    if (trackId === s.trackId) return;
    const q = [...s.queue];
    const at = q.indexOf(trackId);
    let index = s.index;
    if (at !== -1) {
      q.splice(at, 1);
      if (at < index) index--;
      else if (at <= index + this.queuedNext) this.queuedNext--;
    }
    q.splice(index + 1 + this.queuedNext, 0, trackId);
    this.queuedNext++;
    s.queue = q;
    s.index = index;
    this.prefetchNext();
    this.notify();
  }

  /** Append a track to the end of the queue. */
  enqueue(trackId: string) {
    const s = this.state;
    if (!s.trackId) {
      this.load(trackId, [trackId]);
      return;
    }
    if (!s.queue.includes(trackId)) s.queue = [...s.queue, trackId];
    this.prefetchNext();
    this.notify();
  }

  setQueueSource(more: (() => Promise<string[]>) | null) {
    this.source = more;
    this.sourceDone = false;
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
    this.playCause = "resume";
    this.finished = false;
    if (this.state.pendingPlay) {
      this.state.pendingPlay = false;
      this.state.error = "";
    }
    // Play after the end of the queue restarts the last track from its beginning. A stream the watchdog ended
    // (no native `ended`, so play() would not wrap around) otherwise resumes its last fraction of a second and
    // immediately ends again, which re-shows "End of queue" right after the tap.
    if (this.state.endOfQueue) {
      this.pendingSeek = null;
      this.lastPos = 0;
      this.lastProgressAt = this.deps.now();
      this.seek(0);
    }
    this.state.endOfQueue = false;
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
    if (s.index + 1 < s.queue.length) {
      if (this.queuedNext > 0) this.queuedNext--;
      this.goto(s.index + 1);
    }
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
    if (!this.deps.fetchMeta && !this.deps.local) return null;
    const f = this.metaInflight.get(trackId);
    if (f) return f;
    const p = (async () => {
      const offlineNow = this.deps.online ? !this.deps.online() : false;
      // Offline: the local stores answer (no API call). Online the API wins (titles may have changed) and the
      // local copy is the fallback.
      const fromLocal = async () => {
        try {
          return (await this.deps.local?.meta(trackId)) ?? null;
        } catch {
          return null;
        }
      };
      let r: { title: string; album: string } | null = null;
      if (offlineNow) r = await fromLocal();
      if (!r && !(offlineNow && this.deps.local)) {
        try {
          r = (await this.deps.fetchMeta?.(trackId)) ?? null;
        } catch {
          r = null;
        }
      }
      if (!r) r = await fromLocal();
      if (r) this.meta.set(trackId, r);
      return r;
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
    if (queue || !s.queue.includes(trackId)) {
      this.queuedNext = 0;
      this.setQueueSource(opts.more ?? null);
    }
    s.queue = q;
    s.index = q.indexOf(trackId);
    s.trackId = trackId;
    s.noFlac = false;
    s.error = "";
    s.pendingPlay = false;
    s.endOfQueue = false;
    this.finished = false;
    this.playCause = "user";
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
    this.notify();
  }

  /** A downloaded file may be played locally only when the worker controls the page (else the unsigned URL gets a 403). */
  private localEligible(trackId: string): boolean {
    return !!this.deps.local && !!this.deps.hasController?.() && !this.noLocal.has(trackId);
  }

  private async resolveLocal(trackId: string): Promise<LocalSource | null> {
    const l = await Promise.resolve(this.deps.local?.source(trackId))
      .then((x) => x ?? null)
      .catch(() => null);
    this.localInfo.set(trackId, l);
    return l;
  }

  /** Synchronous when the source is known (required for `ended` and gesture-bound play()). */
  private begin(trackId: string) {
    if (this.localEligible(trackId)) {
      const known = this.localInfo.get(trackId);
      if (known) {
        this.applyLocal(trackId, known);
        return;
      }
      if (known === undefined) {
        const unlocked = this.unlock();
        const epoch = this.epoch;
        void this.resolveLocal(trackId).then((l) => {
          if (epoch !== this.epoch) return;
          if (l) this.applyLocal(trackId, l);
          else this.beginRemote(trackId, unlocked);
        });
        return;
      }
    }
    this.beginRemote(trackId, false);
  }

  /** Unlock the element inside the user gesture (iOS/WebKit) before an async step. */
  private unlock(): boolean {
    if (this.autoplay && this.el?.load) {
      this.el.load();
      this.state.playing = !this.el.paused;
      return true;
    }
    return false;
  }

  private beginRemote(trackId: string, unlocked: boolean) {
    const cached = this.cached(trackId);
    if (cached) {
      this.apply(cached);
      return;
    }
    if (!unlocked) this.unlock();
    const epoch = this.epoch;
    void this.links(trackId).then((l) => {
      if (epoch !== this.epoch) return;
      if (l) this.apply(l);
      else this.state.error = "Could not load media links";
    });
  }

  private applyLocal(trackId: string, l: LocalSource) {
    const el = this.el;
    if (!el) return;
    this.state.format = l.format;
    this.state.noFlac = false;
    this.appliedExp = 0;
    this.src = l.src;
    this.appliedTrack = trackId;
    this.playingLocal = trackId;
    el.src = l.src;
    this.state.playing = !el.paused;
    if (this.autoplay) {
      this.wantPlay = true;
      void el.play()?.catch?.((e: unknown) => this.fail(e));
    }
  }

  private apply(links: MediaLinks) {
    const el = this.el;
    if (!el) return;
    this.playingLocal = "";
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

  /** The device's effective format (M1 C22.1), for the download manager (C115.3). */
  deviceFormat(): Format {
    return formatFor(this.deps.device.quality(), this.deps.device.connection());
  }

  private formatFor(trackId: string): Format {
    if (this.forceMp3.has(trackId)) return "mp3";
    return formatFor(this.deps.device.quality(), this.deps.device.connection());
  }

  private fail(e: unknown) {
    // Autoplay rejections are routine (user gesture lost); media errors arrive via `error`.
    if (e instanceof DOMException && e.name === "NotAllowedError") {
      // Keep the intent and the queue position: the next interaction, Media Session action or
      // visibilitychange resumes (watchActivation / resume()).
      this.state.playing = false;
      this.state.pendingPlay = true;
      this.state.error = "Tap to continue";
      this.diag("autoplay_blocked", `cause=${this.playCause}`);
      this.notify();
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

  private prefetchNext(warm = false) {
    const s = this.state;
    const id = s.queue[s.index + 1];
    if (id !== undefined && this.localEligible(id)) {
      // Downloaded: no link to mint. Not downloaded (or the lookup failed): the usual signed-link prefetch.
      void this.resolveLocal(id).then((l) => {
        if (l || id !== s.queue[s.index + 1]) return;
        void this.links(id);
      });
      void this.loadMeta(id);
    } else if (id !== undefined) {
      // cached(): an expired entry is dropped, so this refreshes a link past its half-life.
      void this.links(id).then((l) => {
        if (warm && l && id === s.queue[s.index + 1]) {
          const fmt = l.flac ? this.formatFor(id) : "mp3";
          this.deps.warm?.(fmt === "flac" && l.flac ? l.flac.url : l.mp3.url);
        }
      });
      void this.loadMeta(id);
    }
    if (s.index + 1 >= s.queue.length - 1) this.extendQueue();
  }

  private extendQueue() {
    const src = this.source;
    if (!src || this.sourceBusy || this.sourceDone) return;
    this.sourceBusy = true;
    void src()
      .then((ids) => {
        if (src !== this.source) return;
        const add = ids.filter((i) => !this.state.queue.includes(i));
        if (ids.length === 0) this.sourceDone = true;
        if (add.length) this.state.queue = [...this.state.queue, ...add];
      })
      .catch(() => {})
      .finally(() => {
        if (src === this.source) this.sourceBusy = false;
      });
  }

  private onTimeUpdate() {
    const el = this.el;
    if (!el) return;
    const t = el.currentTime || 0;
    this.state.current = t;
    this.observe(t);
    const d = el.duration;
    // Refresh/warm the next link well before the end so `ended` never has to await.
    if (Number.isFinite(d) && d > 0 && t / d >= PREFETCH_AT && this.prefetchedAt !== this.epoch) {
      this.prefetchedAt = this.epoch;
      this.prefetchNext(true);
    }
  }

  private observe(t: number) {
    if (t !== this.lastPos) {
      this.lastPos = t;
      this.lastProgressAt = this.deps.now();
    }
  }

  private onPlay() {
    const el = this.el;
    this.state.playing = true;
    this.state.endOfQueue = false;
    const d = el?.duration;
    // A replay from the start re-arms the end handling; a play event right at the end (resume at the
    // end) must not, or the same end would be handled twice.
    if (!(d !== undefined && Number.isFinite(d) && d > 0 && d - (el?.currentTime ?? 0) < 1)) this.finished = false;
  }

  private endStall() {
    if (this.stallStart === null) return;
    const ms = this.deps.now() - this.stallStart;
    this.stallStart = null;
    if (ms >= STALL_REPORT_MS) this.diag("stall_end", `ms=${Math.round(ms)}`);
  }

  /** Watchdog (500 ms interval, also on visibilitychange): `ended` did not fire at the end of the stream. */
  tick() {
    const el = this.el;
    const id = this.state.trackId;
    if (!el || !id || this.finished || !this.wantPlay || this.appliedTrack !== id) return;
    const t = el.currentTime || 0;
    this.observe(t);
    const d = el.duration;
    if (!Number.isFinite(d) || !(d > 0) || d - t > END_EPS_S) return;
    if (this.deps.now() - this.lastProgressAt >= END_STALL_MS) this.advance("watchdog");
  }

  private notify() {
    try {
      this.el?.dispatchEvent(new Event("mm:state"));
    } catch {
      /* no DOM */
    }
  }

  private diag(kind: PlayerDiag["kind"], detail?: string, trackId = this.state.trackId, pos?: number) {
    if (!trackId) return;
    const p = pos ?? this.el?.currentTime ?? 0;
    const d: PlayerDiag = { kind, ...(detail ? { detail } : {}), track_id: trackId, pos_ms: Math.max(0, Math.round((Number.isFinite(p) ? p : 0) * 1000)) };
    try {
      this.el?.dispatchEvent(new CustomEvent("mm:diag", { detail: d }));
    } catch {
      /* no DOM */
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
    // An `ended` that arrives while the next track's source is not applied yet is the old stream's.
    if (this.appliedTrack !== this.state.trackId) return;
    // A late `ended` of the stream the watchdog already advanced past: the element now holds the next
    // source, whose `ended` is false (a real end of the current source always reads true).
    if (this.el?.ended === false) return;
    this.advance("ended");
  }

  /** The current track is over (native `ended`, or the watchdog): act exactly once per play. */
  private advance(reason: "ended" | "watchdog") {
    const el = this.el;
    const s = this.state;
    const id = s.trackId;
    if (this.finished || !id) return;
    this.finished = true;
    const pos = el?.currentTime ?? 0;
    // Before anything changes: the listen recorder completes the track that just ended (a track change
    // between listeners would otherwise reset it and the `complete` event would be lost).
    try {
      el?.dispatchEvent(new Event("mm:finish"));
    } catch {
      /* no DOM */
    }
    this.diag("advance", `reason=${reason}`, id, pos);
    s.playing = false;
    const nextId = s.queue[s.index + 1];
    if (nextId === undefined) {
      this.wantPlay = false;
      s.endOfQueue = true;
      s.pendingPlay = false;
      if (reason === "watchdog") el?.pause();
      this.notify();
      return;
    }
    if (this.queuedNext > 0) this.queuedNext--;
    this.epoch++;
    s.index += 1;
    s.trackId = nextId;
    s.noFlac = false;
    s.current = 0;
    s.duration = 0;
    s.pendingPlay = false;
    s.error = "";
    this.pendingSeek = null;
    this.autoplay = true;
    this.wantPlay = true;
    this.playCause = "advance";
    this.finished = false;
    this.lastPos = 0;
    this.lastProgressAt = this.deps.now();
    this.probePending.delete(nextId);
    this.reminted.delete(nextId);
    this.syncMeta(nextId);
    this.begin(nextId);
    this.notify();
  }

  private onError() {
    const el = this.el;
    const code = el?.error?.code;
    const id = this.state.trackId;
    if (!el || !id) return;
    this.diag("error", `code=${code ?? 0},fmt=${this.state.format}${/MediaSink/i.test(el.error?.message ?? "") ? ",sink=1" : ""}`);
    // Firefox reports a failing audio output (no device, e.g. headless CI) as MEDIA_ERR_DECODE with this message
    // after decoding already worked, and keeps the media clock running. It says nothing about the codec: do not
    // fall back to MP3, record a failed probe, or touch playback state.
    if (/MediaSink/i.test(el.error?.message ?? "")) return;
    const at = el.currentTime || this.state.current || 0;
    const resume = this.wantPlay;
    if (this.playingLocal === id && this.appliedTrack === id) {
      // Any error on a local source (the network answers an unsigned URL with 403, which browsers may report as
      // MEDIA_ERR_SRC_NOT_SUPPORTED): the file is suspect. Mint a signed link once and resume at the same position.
      // This is not a codec failure: no FLAC fallback, no failed probe, no put_device_profile (C115.4).
      this.playingLocal = "";
      this.noLocal.add(id);
      this.localInfo.set(id, null);
      this.deps.local?.markSuspect(id);
      this.cache.delete(id);
      const epoch = this.epoch;
      void this.links(id, true).then((l) => {
        if (epoch !== this.epoch) return;
        if (!l) {
          this.state.error = "Could not load media links";
          return;
        }
        this.pendingSeek = at > 0 ? at : null;
        this.autoplay = resume;
        this.apply(l);
      });
      return;
    }
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

// One hidden element warms the first bytes (metadata) of the next track's signed URL; it is never played.
let warmEl: HTMLAudioElement | null = null;
function warm(url: string) {
  if (typeof Audio === "undefined") return;
  warmEl ??= Object.assign(new Audio(), { preload: "metadata", muted: true });
  if (warmEl.src !== new URL(url, location.href).href) warmEl.src = url;
}

export const player = new Player({
  fetchLinks: async (id) => {
    const r = await api.GET("/api/tracks/{id}/media", { params: { path: { id } } });
    return r.data ?? null;
  },
  device: localDevice,
  now: () => Date.now(),
  fetchMeta: (id) => resolveMeta(id, getTrackRef),
  warm,
  local: offline.hooks,
  hasController: () => typeof navigator !== "undefined" && !!navigator.serviceWorker?.controller,
  online: () => typeof navigator === "undefined" || navigator.onLine !== false,
});

session.configure({ isPlaying: () => player.state.playing });
offline.setDeviceFormat(() => player.deviceFormat());

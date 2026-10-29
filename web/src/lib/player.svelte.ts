// Global player store (C22.1). The single audio element lives in App.svelte's footer and is
// handed over by attach(); it is never re-created on navigation.
import { playerInUse } from "./playerUse";
import { dbToGain, fetchRecordGains, isTapeItem, recordIdOfHref, tapeMediaLinks, tapeMeta } from "./recordMedia";
import { getAudioContext } from "./audioContext";
import { api } from "../api/client";
import { session } from "../api/session";
import { offline } from "./offline.svelte";
import type { LocalSource } from "../api/offline";
import type { components } from "../api/schema";
import { createAudioFocus, type Lease, type LeaseHandlers, type LeaseOptions } from "./audioFocus";
import { pushToast } from "./toast.svelte";
import type { PlayerSnapshot, SavedPlayer, SavedSource } from "./playerStore";

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
  /** A queue change or undo offer to announce (live region + toast); absent: silent. */
  notice?(message: string, action?: { label: string; run(): void }): void;
  /** Inside a user gesture (transient activation)? Decides whether a lease release may resume at once. */
  inGesture?(): boolean;
  /** The shared AudioContext for the gain stage (gained plays only); absent: the real one. Throws when Web Audio is missing. */
  audioContext?(): AudioContext;
  /** The plan gains of the Record behind a restored queue's source href (`/records/:id`); absent: no rebuild. */
  rebuildGains?(href: string): Promise<Record<string, number> | null>;
}

export interface PlayOptions {
  title?: string;
  album?: string;
  /** Same as `album` (the secondary line); wins when both are given. Lets L2 sources avoid the Subsonic word. */
  subtitle?: string;
  /** Seconds; applied in `loadedmetadata` (iOS ignores earlier writes). */
  startAt?: number;
  /** false: load paused (deep link). Default true. */
  autoplay?: boolean;
  /** Fetches more queue entries (the next list page); asked when the queue is about to run out. */
  more?: () => Promise<string[]>;
  /** Where the queue came from ("Playing from <label>", linking to href). Kept when the queue is only repositioned. */
  source?: SavedSource;
  /** An explicit replacement of a loaded queue: offer "Queue replaced · Undo" (backed by restoreQueue). */
  undo?: boolean;
  /** take_id -> gain in dB, applied through one GainNode on the single element at each track start. In memory only. */
  gains?: Record<string, number>;
}

/** Everything restoreQueue needs to put a replaced queue back. */
export interface QueueSnapshot {
  queue: string[];
  index: number;
  queuedNext: number;
  trackId: string;
  position: number;
  wasPlaying: boolean;
  source: SavedSource | null;
  more: (() => Promise<string[]>) | null;
  gains: Record<string, number> | null;
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
/** After a dead-output error the media clock may only reach the end this many seconds early (clock jitter) before an `ended` is believed. */
const SINK_END_SLACK_S = 3;
/** How often one track may be reloaded after an `ended` that followed a dead-output error (a really dead device must still let the queue move on). */
const SINK_RECOVERIES = 2;
const LOADING_TITLE = "Loading…";
const FALLBACK_TITLE = "Untitled track";

export class Player {
  el = $state<HTMLAudioElement | null>(null);
  title = $state("Nothing playing");
  album = $state("");
  /**
   * A blind Judge card hides the take's identity outside the page too: while set, the Media Session shows this label
   * ("Take A") instead of the title, album and cover.
   */
  mask = $state<string | null>(null);
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
    /** "Playing from <label>" for the queue. */
    source: null as SavedSource | null,
    /** Restored from the last session, paused at this position (s): "Resume <title> at m:ss" needs one tap. */
    restored: null as number | null,
    /** A duel (audio focus lease) paused the player and gave the output back outside a gesture: "Resume <title>". */
    held: false,
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
  /** Reloads done per track after an `ended` that followed a sink error (see prematureSinkEnd). */
  private sinkRecoveries = new Map<string, number>();
  /* eslint-enable svelte/prefer-svelte-reactivity */
  private cleanup: (() => void) | null = null;
  /** expires_at (ms) of the link currently applied to the element. */
  private appliedExp = 0;
  /** Track whose source on the element is its local file (a media error then re-mints a signed link). */
  private playingLocal = "";
  /** Track whose links were last applied to the element: only its metadata may consume a pending seek. */
  private appliedTrack = "";
  /** Firefox reported a failing audio output (`MediaSink` error) on this track: where, and when (deps.now). Cleared by any seek or track change. */
  private sinkErr: { id: string; at: number; t: number } | null = null;
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
  /** The queue's gain map (take_id -> dB); never persisted, rebuilt for a restored `/records/:id` queue. */
  private gains: Record<string, number> | null = null;
  /** The one gain stage: the element's MediaElementAudioSourceNode -> GainNode -> destination, built on the first gained play. */
  private stage: { ctx: AudioContext; gain: GainNode } | null = null;
  private stageFailed = false;
  private focus = createAudioFocus({
    wasPlaying: () => this.wantPlay,
    trackId: () => this.state.trackId,
    // Not a user pause: the intent is remembered by the lease, and the lock screen must not offer a stale "pause".
    suspend: () => {
      this.wantPlay = false;
      this.autoplay = false;
      this.el?.pause();
    },
    resume: () => this.resume(),
    hold: (on) => {
      this.state.held = on;
    },
    inGesture: () => (this.deps.inGesture ? this.deps.inGesture() : false),
    changed: () => this.notify(),
  });

  constructor(deps: PlayerDeps) {
    this.deps = deps;
  }

  /**
   * Borrow the audio output (a duel, an A/B comparison): the player pauses, and release({resume:true}) gives it back,
   * resuming at once inside a gesture or leaving "Resume <title>" in the mini-player. Any user transport action on
   * the player ends the lease. The lock screen's play/pause control the owner's handlers meanwhile.
   */
  acquireFocus(owner: string, o?: LeaseOptions): Lease {
    return this.focus.acquire(owner, o);
  }

  /** The Media Session handlers of the current lease owner, or null when the player owns the transport. */
  get leaseHandlers(): LeaseHandlers | null {
    return this.focus.handlers();
  }

  private say(message: string, action?: { label: string; run(): void }) {
    this.deps.notice?.(message, action);
  }

  /** "<verb>: <title>" once the title is known (cached or fetched); never blocks the queue change itself. */
  private sayTitled(verb: string, id: string) {
    if (!this.deps.notice) return;
    const known = this.meta.get(id)?.title;
    if (known) return this.say(`${verb}: ${known}`);
    void this.loadMeta(id)
      .catch(() => null)
      .then((m) => this.say(`${verb}: ${m?.title ?? "this take"}`));
  }

  /** The queue as it is now, for a later restoreQueue. */
  snapshotQueue(): QueueSnapshot {
    const s = this.state;
    return {
      queue: [...s.queue],
      index: s.index,
      queuedNext: this.queuedNext,
      trackId: s.trackId,
      position: s.current,
      wasPlaying: this.wantPlay,
      source: s.source,
      more: this.source,
      gains: this.gains,
    };
  }

  /** Put a replaced queue back (the Undo of "Queue replaced"). Runs from a click: it may start playback. */
  restoreQueue(p: QueueSnapshot) {
    if (!p.trackId || !p.queue.includes(p.trackId)) return;
    this.deps.device.onGesture?.();
    this.start(p.trackId, p.queue, { startAt: p.position, autoplay: p.wasPlaying, ...(p.more ? { more: p.more } : {}), ...(p.source ? { source: p.source } : {}), ...(p.gains ? { gains: p.gains } : {}) });
    this.queuedNext = Math.max(0, Math.min(p.queuedNext, p.queue.length - 1 - this.state.index));
    this.say("Queue restored");
  }

  /** The persistable part of the state (ids and numbers only): see playerStore.ts. */
  snapshot(): PlayerSnapshot {
    const s = this.state;
    return {
      queue: [...s.queue],
      index: s.index,
      queuedNext: this.queuedNext,
      position: s.current,
      trackId: s.trackId,
      source: s.source,
    };
  }

  /**
   * Boot restore (D2): the saved queue comes back PAUSED at its position; nothing plays and no media is fetched
   * but the link of the current track, minted ahead so the one tap that resumes can apply it synchronously.
   */
  restore(v: SavedPlayer): boolean {
    const s = this.state;
    if (s.trackId || !v.trackId) return false;
    this.epoch++;
    this.queuedNext = v.queuedNext;
    this.setQueueSource(null);
    s.queue = [...v.queue];
    s.index = v.queue.indexOf(v.trackId);
    s.trackId = v.trackId;
    s.source = v.source;
    s.current = v.position;
    s.duration = 0;
    s.restored = v.position;
    s.playing = false;
    s.pendingPlay = false;
    s.endOfQueue = false;
    s.error = "";
    this.autoplay = false;
    this.wantPlay = false;
    this.pendingSeek = v.position > 0 ? v.position : null;
    this.syncMeta(v.trackId);
    void this.links(v.trackId);
    this.rebuildGains(v.source);
    this.notify();
    return true;
  }

  /**
   * The page became visible again: a long-paused track's signed link may have expired meanwhile. Mint it now (into
   * the cache only) so the next tap applies it inside the gesture. A playing track is never touched: its src is
   * never swapped from under it.
   */
  prewarmPaused() {
    const s = this.state;
    if (!s.trackId || s.playing || this.wantPlay) return;
    if (this.localEligible(s.trackId) && this.localInfo.get(s.trackId)) return;
    if (this.cached(s.trackId)) return;
    void this.links(s.trackId);
  }

  configure(d: Partial<PlayerDeps>) {
    this.deps = { ...this.deps, ...d };
  }

  attach(el: HTMLAudioElement | null) {
    this.cleanup?.();
    this.cleanup = null;
    this.el = el;
    if (!el) return;
    const on = (type: string, fn: (e: Event) => void) => {
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
      on("ended", (e) => this.onEnded(e)),
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
      this.prewarmPaused();
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
  playNext(trackId: string, quiet = false) {
    const s = this.state;
    if (!s.trackId) {
      this.load(trackId, [trackId]);
      if (!quiet) this.sayTitled("Plays next", trackId);
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
    if (!quiet) this.sayTitled("Plays next", trackId);
  }

  /** "Add to queue": append a track to the end; already queued (not the current one): move it to the end. */
  enqueue(trackId: string, quiet = false) {
    const s = this.state;
    if (!s.trackId) {
      this.load(trackId, [trackId]);
      if (!quiet) this.sayTitled("Added to queue", trackId);
      return;
    }
    if (trackId === s.trackId) return quiet ? undefined : this.sayTitled("Already playing", trackId);
    const q = [...s.queue];
    const at = q.indexOf(trackId);
    let index = s.index;
    if (at !== -1) {
      q.splice(at, 1);
      if (at < index) index--;
      else if (at <= index + this.queuedNext) this.queuedNext--;
    }
    q.push(trackId);
    s.queue = q;
    s.index = index;
    this.prefetchNext();
    this.notify();
    if (!quiet) this.sayTitled(at !== -1 ? "Moved to the end of the queue" : "Added to queue", trackId);
  }

  /** Remove an upcoming (or already played) entry; the current track cannot be removed. */
  remove(i: number): boolean {
    const s = this.state;
    if (!Number.isInteger(i) || i < 0 || i >= s.queue.length || i === s.index) return false;
    const id = s.queue[i]!;
    const q = [...s.queue];
    q.splice(i, 1);
    if (i < s.index) s.index--;
    else if (i <= s.index + this.queuedNext) this.queuedNext--;
    s.queue = q;
    this.prefetchNext();
    this.notify();
    this.sayTitled("Removed from the queue", id);
    return true;
  }

  /**
   * Move an upcoming entry to position `to` (its index after the move). Both positions are after the current
   * track; the current track and the history stay put. An entry moved into the "play next" run joins it, one
   * moved out of it leaves it.
   */
  move(from: number, to: number): boolean {
    const s = this.state;
    const n = s.queue.length;
    if (!Number.isInteger(from) || !Number.isInteger(to) || from === to) return false;
    if (from <= s.index || to <= s.index || from >= n || to >= n) return false;
    const id = s.queue[from]!;
    const q = [...s.queue];
    q.splice(from, 1);
    let qn = this.queuedNext;
    if (from <= s.index + qn) qn--;
    q.splice(to, 0, id);
    if (to <= s.index + qn) qn++;
    s.queue = q;
    this.queuedNext = qn;
    this.prefetchNext();
    this.notify();
    this.sayTitled(to < from ? "Moved up" : "Moved down", id);
    return true;
  }

  /** Drop everything after the current track (and the source that would refill it). */
  clearUpcoming(): boolean {
    const s = this.state;
    if (s.index < 0 || s.index + 1 >= s.queue.length) return false;
    const dropped = s.queue.length - s.index - 1;
    s.queue = s.queue.slice(0, s.index + 1);
    this.queuedNext = 0;
    this.setQueueSource(null);
    this.sourceDone = true;
    this.notify();
    this.say(dropped === 1 ? "Cleared 1 upcoming take" : `Cleared ${dropped} upcoming takes`);
    return true;
  }

  setQueueSource(more: (() => Promise<string[]>) | null) {
    this.source = more;
    this.sourceDone = false;
  }

  /** Start playing a track (optionally with a queue it belongs to). Must be called from a user gesture. */
  play(trackId: string, queue?: string[], opts: PlayOptions = {}) {
    this.deps.device.onGesture?.();
    const s = this.state;
    const replaces = !!opts.undo && s.trackId !== "" && trackId !== s.trackId && (queue !== undefined || !s.queue.includes(trackId));
    const prev = replaces ? this.snapshotQueue() : null;
    this.start(trackId, queue, opts);
    if (prev) this.say("Queue replaced", { label: "Undo", run: () => this.restoreQueue(prev) });
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
    this.focus.interrupt();
    this.wantPlay = false;
    this.autoplay = false;
    this.el?.pause();
  }

  /** User resume (toggle, Media Session 'play'). */
  resume() {
    const el = this.el;
    if (!el || !this.state.trackId) return;
    this.deps.device.onGesture?.();
    this.focus.interrupt();
    this.state.held = false;
    this.state.restored = null;
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
    // A tap is the one place a restored queue's rebuilt gains may build the stage (a context made outside a gesture stays suspended on iOS).
    if (this.gains) this.ensureStage();
    this.wakeStage();
    if (el.error || this.appliedTrack !== this.state.trackId || (el.readyState === 0 && !this.cached(this.state.trackId))) {
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
    this.sinkErr = null; // a deliberate move: the clock estimate of prematureSinkEnd no longer holds
    el.currentTime = t;
    this.state.current = t;
  }

  next(startAt?: number) {
    const s = this.state;
    if (s.index + 1 < s.queue.length) {
      if (this.queuedNext > 0) this.queuedNext--;
      this.goto(s.index + 1, startAt);
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

  setMask(label: string | null) {
    if (this.mask === label) return;
    this.mask = label;
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

  private goto(index: number, startAt?: number) {
    const id = this.state.queue[index];
    if (id === undefined) return;
    this.start(id, this.state.queue, { autoplay: true, ...(startAt !== undefined ? { startAt } : {}) }, true);
  }

  /** `reposition`: the same queue, another entry (next/prev): the gain map stays. */
  private start(trackId: string, queue: string[] | undefined, opts: PlayOptions, reposition = false) {
    const s = this.state;
    this.focus.interrupt();
    this.epoch++;
    const q = queue && queue.includes(trackId) ? [...queue] : queue ? [trackId] : s.queue.includes(trackId) ? s.queue : [trackId];
    if (queue || !s.queue.includes(trackId)) {
      this.queuedNext = 0;
      this.setQueueSource(opts.more ?? null);
      s.source = opts.source ?? null;
      if (!reposition || opts.gains) this.gains = opts.gains ?? null;
    } else {
      if (opts.source) s.source = opts.source;
      if (opts.gains) this.gains = opts.gains;
    }
    // Inside the user gesture: building the stage and resuming its context must not wait for the async link fetch.
    if (this.gains && Object.keys(this.gains).length > 0) this.ensureStage();
    this.wakeStage();
    s.held = false;
    s.restored = null;
    s.queue = q;
    s.index = q.indexOf(trackId);
    s.trackId = trackId;
    s.noFlac = false;
    s.error = "";
    s.pendingPlay = false;
    s.endOfQueue = false;
    this.finished = false;
    this.sinkErr = null;
    this.sinkRecoveries.delete(trackId);
    this.playCause = "user";
    s.current = opts.startAt ?? 0;
    s.duration = 0;
    this.pendingSeek = opts.startAt != null && opts.startAt > 0 ? opts.startAt : null;
    this.autoplay = opts.autoplay !== false;
    this.wantPlay = this.autoplay;
    this.probePending.delete(trackId);
    this.reminted.delete(trackId);
    // Cache only a complete pair; a title alone is shown at once while the album still resolves.
    const second = opts.subtitle ?? opts.album;
    if (opts.title && second !== undefined) this.meta.set(trackId, { title: opts.title, album: second });
    this.syncMeta(trackId, opts.title);
    this.begin(trackId);
    this.notify();
  }

  /** Replace the gain map of the current queue (null: none). Applies to the next track start. */
  setGains(gains: Record<string, number> | null) {
    this.gains = gains && Object.keys(gains).length > 0 ? gains : null;
    if (this.gains) {
      this.ensureStage();
      this.wakeStage();
    }
  }

  /** Add gains for tracks appended to the current queue ("Add to queue" of a Record). */
  mergeGains(gains: Record<string, number>) {
    this.setGains({ ...(this.gains ?? {}), ...gains });
  }

  /** A restored queue whose source is a Record: its plan gains are not stored, so read them again. */
  private rebuildGains(source: SavedSource | null) {
    const href = source?.href;
    if (!href || !this.deps.rebuildGains || !recordIdOfHref(href)) return;
    const epoch = this.epoch;
    void this.deps
      .rebuildGains(href)
      .then((g) => {
        if (!g || epoch !== this.epoch || this.state.source?.href !== href || this.gains) return;
        // Stored only: the stage is built by the resume tap (resume()), never at boot.
        this.gains = Object.keys(g).length > 0 ? g : null;
      })
      .catch(() => {});
  }

  /**
   * The single gain stage (C131.5): created lazily on the first gained play, never one per track, and left in the
   * graph at unity for the page's life. iOS ignores audio.volume, so Web Audio gain is the only matching path.
   */
  private ensureStage(): { ctx: AudioContext; gain: GainNode } | null {
    if (this.stage) return this.stage;
    const el = this.el;
    if (!el || this.stageFailed) return null;
    try {
      const ctx = (this.deps.audioContext ?? getAudioContext)();
      const gain = ctx.createGain();
      ctx.createMediaElementSource(el).connect(gain);
      gain.connect(ctx.destination);
      this.stage = { ctx, gain };
    } catch {
      // No Web Audio (or the element is already routed): play unmatched rather than not at all.
      this.stageFailed = true;
      return null;
    }
    return this.stage;
  }

  /** A routed element is silent while its context is suspended (A/B suspends the shared one, iOS interrupts it). */
  private wakeStage() {
    const ctx = this.stage?.ctx;
    if (ctx && ctx.state !== "running") void Promise.resolve(ctx.resume()).catch(() => {});
  }

  /** Apply the track's gain at its start: the map value, else unity. No stage (no gained play yet): nothing. */
  private setGain(trackId: string) {
    // Never builds the stage: apply() may run outside a gesture, and a context created there stays suspended (silent) on iOS.
    const st = this.stage;
    if (!st) return;
    const db = this.gains?.[trackId];
    const v = db !== undefined && Number.isFinite(db) ? dbToGain(db) : 1;
    st.gain.gain.setValueAtTime(v, st.ctx.currentTime);
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
    this.setGain(trackId);
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
    this.setGain(links.track_id);
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
    if (isTapeItem(trackId)) return "flac";
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
    this.wakeStage();
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

  private onEnded(e?: Event) {
    // An `ended` that arrives while the next track's source is not applied yet is the old stream's.
    if (this.appliedTrack !== this.state.trackId) return;
    // A late `ended` of the stream the watchdog already advanced past: the element now holds the next
    // source, whose `ended` is false (a real end of the current source always reads true).
    if (this.el?.ended === false) return;
    if (this.prematureSinkEnd()) {
      // Not a finish: keep the listen recorder (a later listener on the same element) and the Media Session from
      // treating it as one.
      e?.stopImmediatePropagation();
      return;
    }
    this.advance("ended");
  }

  /**
   * An `ended` after a Firefox audio-output error that the media clock cannot have reached yet is the dead output's
   * stream giving up, not the end of the song (the listener hears the song stop and the queue skip ahead). Reload the
   * same source where the error hit instead of advancing. Bounded: a device that stays dead still moves on.
   * Headless Firefox without any audio device raises the same error but its clock runs to the real end, which this
   * lets through (the clock needs `duration - at` seconds to get there).
   */
  private prematureSinkEnd(): boolean {
    const el = this.el;
    const s = this.sinkErr;
    const id = this.state.trackId;
    if (!el || !s || s.id !== id) return false;
    const d = el.duration;
    if (!Number.isFinite(d) || !(d > 0)) return false;
    const clockS = (this.deps.now() - s.t) / 1000;
    if (clockS >= d - s.at - SINK_END_SLACK_S) return false;
    const n = this.sinkRecoveries.get(id) ?? 0;
    if (n >= SINK_RECOVERIES) return false;
    this.sinkRecoveries.set(id, n + 1);
    this.sinkErr = null;
    this.diag("error", `sink_end=1,at=${Math.round(s.at * 1000)}`);
    this.pendingSeek = s.at > 0 ? s.at : null;
    this.autoplay = true;
    this.wantPlay = true;
    this.finished = false;
    this.epoch++;
    this.begin(id);
    return true;
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
    if (/MediaSink/i.test(el.error?.message ?? "")) {
      // Remember it: the output may be dead although the clock runs on (see prematureSinkEnd).
      if (this.sinkErr?.id !== id) this.sinkErr = { id, at: el.currentTime || this.state.current || 0, t: this.deps.now() };
      return;
    }
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
      this.state.format === "flac" &&
      !isTapeItem(id)
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
    if (isTapeItem(id)) return tapeMediaLinks(id);
    const r = await api.GET("/api/tracks/{id}/media", { params: { path: { id } } });
    return r.data ?? null;
  },
  device: localDevice,
  now: () => Date.now(),
  fetchMeta: async (id) => {
    if (!isTapeItem(id)) return resolveMeta(id, getTrackRef);
    const m = await tapeMeta(id);
    return m ? { title: m.title, album: m.subtitle } : null;
  },
  rebuildGains: (href) => {
    const id = recordIdOfHref(href);
    return id ? fetchRecordGains(id) : Promise.resolve(null);
  },
  warm,
  notice: (message, action) => void pushToast(message, { kind: "info", ...(action ? { action, ms: 8000 } : {}) }),
  inGesture: () => typeof navigator !== "undefined" && !!(navigator as Navigator & { userActivation?: { isActive: boolean } }).userActivation?.isActive,
  local: offline.hooks,
  hasController: () => typeof navigator !== "undefined" && !!navigator.serviceWorker?.controller,
  online: () => typeof navigator === "undefined" || navigator.onLine !== false,
});

session.configure({ isPlaying: () => playerInUse(player.state) });
offline.setDeviceFormat(() => player.deviceFormat());

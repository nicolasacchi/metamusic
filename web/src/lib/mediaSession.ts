// Media Session glue (C22.2): lock-screen metadata, transport handlers, position state.
import { isTapeItem } from "./recordMedia";
import { artworkList, type CoverSet } from "./coverLinks";

export interface MSPlayer {
  title: string;
  album: string;
  /** Blind listening: this label replaces the title, album and cover on the lock screen. */
  mask?: string | null;
  state: { trackId: string; endOfQueue?: boolean; pendingPlay?: boolean };
  /** A next track exists (undefined: assume yes). */
  hasNext?: boolean;
  /** The user wants playback (survives the element being paused during a src swap). */
  playIntent?: boolean;
  /** Audio focus lease owner's transport (a duel): while set, play/pause control it and prev/next do nothing. */
  leaseHandlers?: { play(): void; pause(): void; playing(): boolean } | null;
  prev(): void;
  next(): void;
  seek(s: number): void;
  resume(): void;
  pause(): void;
}

export interface MSEnv {
  mediaSession?: {
    metadata: unknown;
    playbackState?: string;
    setActionHandler(action: string, h: ((d: { seekTime?: number }) => void) | null): void;
    setPositionState(s?: { duration: number; position: number; playbackRate: number }): void;
  };
  MediaMetadata?: new (init: unknown) => unknown;
  doc: Pick<Document, "addEventListener" | "removeEventListener" | "visibilityState">;
  win: Pick<Window, "addEventListener" | "removeEventListener">;
  now?: () => number;
  /** Cover links of a track (C115: online 512 + 1024, offline the cached 512); absent or null keeps the icon. */
  cover?: (trackId: string) => Promise<CoverSet | null>;
}

/** The browser environment, plus the cover provider (App passes the real one). */
export function mediaSessionEnv(cover?: MSEnv["cover"]): MSEnv {
  return {
    cover,
    mediaSession: (navigator as unknown as { mediaSession?: MSEnv["mediaSession"] }).mediaSession,
    MediaMetadata: (globalThis as unknown as { MediaMetadata?: MSEnv["MediaMetadata"] }).MediaMetadata,
    doc: document,
    win: window,
  };
}

/** Wires the Media Session to the player; returns a cleanup. No-op where unsupported. */
export function installMediaSession(player: MSPlayer, el: HTMLAudioElement, env: MSEnv = mediaSessionEnv()): () => void {
  const ms = env.mediaSession;
  if (!ms) return () => {};
  const now = env.now ?? Date.now;
  let lastPos = 0;

  const register = () => {
    const set = (a: string, h: ((d: { seekTime?: number }) => void) | null) => {
      try {
        ms.setActionHandler(a, h);
      } catch {
        /* action unsupported by this browser */
      }
    };
    set("play", () => (player.leaseHandlers ? player.leaseHandlers.play() : player.resume()));
    set("pause", () => (player.leaseHandlers ? player.leaseHandlers.pause() : player.pause()));
    set("previoustrack", () => {
      if (!player.leaseHandlers) player.prev();
    });
    set("nexttrack", player.hasNext === false ? null : () => {
      if (!player.leaseHandlers) player.next();
    });
    set("seekto", (d) => {
      if (typeof d.seekTime === "number") player.seek(d.seekTime);
    });
    set("seekforward", null);
    set("seekbackward", null);
  };

  // Artwork resolved for one track; the icon shows until it arrives.
  let art: { id: string; list: ReturnType<typeof artworkList> } | null = null;
  let asked = "";
  const wantArt = () => {
    const id = player.state.trackId;
    if (!env.cover || !id || isTapeItem(id) || asked === id || player.mask) return;
    asked = id;
    void env
      .cover(id)
      .then((c) => (c ? artworkList(c) : null))
      .catch(() => null)
      .then((list) => {
        if (list && player.state.trackId === id) {
          art = { id, list };
          metadata();
        } else if (!list && asked === id) {
          asked = ""; // a later event may retry (links can come back)
        }
      });
  };

  const metadata = () => {
    if (!env.MediaMetadata) return;
    const masked = player.mask ?? null;
    ms.metadata = new env.MediaMetadata({
      title: masked ?? player.title,
      artist: "metamusic",
      album: masked !== null ? "" : player.album,
      artwork: masked === null && art && art.id === player.state.trackId ? art.list : artworkList(null),
    });
    wantArt();
  };

  const position = () => {
    const d = el.duration;
    if (!Number.isFinite(d) || !(d > 0)) return;
    const pos = Math.min(Math.max(0, Number.isFinite(el.currentTime) ? el.currentTime : 0), d);
    const rate = el.playbackRate > 0 ? el.playbackRate : 1;
    try {
      ms.setPositionState({ duration: d, position: pos, playbackRate: rate });
    } catch {
      /* browsers throw on inconsistent state; position display is best-effort */
    }
  };

  const clearPosition = () => {
    try {
      ms.setPositionState();
    } catch {
      /* best-effort */
    }
  };

  /** playbackState drives the Android notification: stale values show the wrong transport button. */
  const state = () => {
    const st = player.state;
    let v: string;
    const lease = player.leaseHandlers;
    if (lease) v = lease.playing() ? "playing" : "paused";
    else if (st.endOfQueue) v = "none";
    else if (st.pendingPlay) v = "paused";
    else v = (player.playIntent ?? !el.paused) ? "playing" : "paused";
    try {
      ms.playbackState = v;
    } catch {
      /* optional */
    }
  };

  let shown = player.state.trackId;
  /** Player state changed (track transition, queue edit, blocked autoplay, end of queue). */
  const onState = () => {
    register();
    if (player.state.trackId !== shown) {
      shown = player.state.trackId;
      clearPosition();
      metadata();
    }
    state();
  };

  const onLoaded = () => {
    register();
    metadata();
    position();
  };
  const onPlaying = () => {
    register();
    metadata();
    state();
  };
  const onSeeked = () => position();
  const onTime = () => {
    const t = now();
    if (t - lastPos < 1000) return;
    lastPos = t;
    position();
  };
  const onVisible = () => {
    if (env.doc.visibilityState === "visible") register();
  };
  const onPageShow = () => register();

  register();
  el.addEventListener("loadedmetadata", onLoaded);
  el.addEventListener("playing", onPlaying);
  el.addEventListener("mm:meta", metadata);
  el.addEventListener("seeked", onSeeked);
  el.addEventListener("timeupdate", onTime);
  const transport = ["play", "pause", "ended", "emptied"] as const;
  for (const t of transport) el.addEventListener(t, state);
  el.addEventListener("mm:state", onState);
  env.doc.addEventListener("visibilitychange", onVisible);
  env.win.addEventListener("pageshow", onPageShow);
  return () => {
    el.removeEventListener("loadedmetadata", onLoaded);
    el.removeEventListener("playing", onPlaying);
    el.removeEventListener("mm:meta", metadata);
    el.removeEventListener("seeked", onSeeked);
    el.removeEventListener("timeupdate", onTime);
    for (const t of transport) el.removeEventListener(t, state);
    el.removeEventListener("mm:state", onState);
    env.doc.removeEventListener("visibilitychange", onVisible);
    env.win.removeEventListener("pageshow", onPageShow);
  };
}

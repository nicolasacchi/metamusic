// Media Session glue (C22.2): lock-screen metadata, transport handlers, position state.

export interface MSPlayer {
  title: string;
  album: string;
  state: { trackId: string; endOfQueue?: boolean; pendingPlay?: boolean };
  /** A next track exists (undefined: assume yes). */
  hasNext?: boolean;
  /** The user wants playback (survives the element being paused during a src swap). */
  playIntent?: boolean;
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
}

function defaultEnv(): MSEnv {
  return {
    mediaSession: (navigator as unknown as { mediaSession?: MSEnv["mediaSession"] }).mediaSession,
    MediaMetadata: (globalThis as unknown as { MediaMetadata?: MSEnv["MediaMetadata"] }).MediaMetadata,
    doc: document,
    win: window,
  };
}

/** Wires the Media Session to the player; returns a cleanup. No-op where unsupported. */
export function installMediaSession(player: MSPlayer, el: HTMLAudioElement, env: MSEnv = defaultEnv()): () => void {
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
    set("play", () => player.resume());
    set("pause", () => player.pause());
    set("previoustrack", () => player.prev());
    set("nexttrack", player.hasNext === false ? null : () => player.next());
    set("seekto", (d) => {
      if (typeof d.seekTime === "number") player.seek(d.seekTime);
    });
    set("seekforward", null);
    set("seekbackward", null);
  };

  const metadata = () => {
    if (!env.MediaMetadata) return;
    ms.metadata = new env.MediaMetadata({
      title: player.title,
      artist: "metamusic",
      album: player.album,
      artwork: [{ src: "/icons/icon-512.png", sizes: "512x512", type: "image/png" }],
    });
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
    if (st.endOfQueue) v = "none";
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

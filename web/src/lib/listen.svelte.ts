// Listening recorder (C40.3): turns player/element events into listen events for the outbox.
// The core (createRecorder) is plain and takes an injectable PlayerLike so tests can fake it.
import { untrack } from "svelte";
import { newEventId, outbox, startOutbox, type ListenEvent } from "../api/outbox";
import { getDevice } from "./device.svelte";
import { uuidv7 } from "./id";
import type { Player } from "./player.svelte";

export type { ListenEvent };

export interface PlayerLike {
  el: EventTarget | null;
  state: { trackId: string; format: "flac" | "mp3" | string; duration: number };
}

interface ElLike extends EventTarget {
  currentTime: number;
  duration: number;
}

export interface RecorderDeps {
  player: PlayerLike;
  emit(e: ListenEvent): void;
  sessionId: string;
  deviceId(): string | undefined;
  id(): string;
  now(): Date;
}

const SKIP_BELOW = 0.9;
const TICK_MS = 30_000;
/** A seek smaller than this is timeupdate staleness, not a user seek. */
const MIN_SEEK_S = 0.5;
const ms = (s: number) => Math.max(0, Math.round(s * 1000));

export function createRecorder(deps: RecorderDeps) {
  const el = deps.player.el as ElLike | null;
  let track = deps.player.state.trackId;
  let started = false;
  let completed = false;
  let playing = false;
  let segStart: number | null = null; // seconds
  let lastPos = 0;
  let heard = 0; // seconds in closed segments of the current track
  let duration = 0;

  function emit(kind: ListenEvent["kind"], extra: Partial<ListenEvent>) {
    const dev = deps.deviceId();
    const fmt = deps.player.state.format;
    deps.emit({
      id: deps.id(),
      track_id: track,
      session_id: deps.sessionId,
      kind,
      ...(fmt === "flac" || fmt === "mp3" ? { format: fmt } : {}),
      ...(dev ? { device_id: dev } : {}),
      client_at: deps.now().toISOString(),
      ...extra,
    });
  }

  function closeSegment(to: number) {
    if (segStart === null) return;
    if (to > segStart) {
      emit("segment", { from_ms: ms(segStart), to_ms: ms(to) });
      heard += to - segStart;
    }
    segStart = null;
  }

  function reset(next: string) {
    track = next;
    started = false;
    completed = false;
    playing = false;
    segStart = null;
    heard = 0;
    lastPos = 0;
    duration = 0;
  }

  /** Called whenever the player's track may have changed (and at the start of every handler). */
  function sync() {
    const now = deps.player.state.trackId;
    if (now === track) return;
    closeSegment(lastPos);
    if (started && !completed && duration > 0 && heard < SKIP_BELOW * duration) {
      emit("skip", { from_ms: ms(lastPos) });
    }
    reset(now);
  }

  function pos(): number {
    return el && Number.isFinite(el.currentTime) ? el.currentTime : lastPos;
  }
  function dur(): number {
    const d = el?.duration;
    if (d !== undefined && Number.isFinite(d) && d > 0) return d;
    return deps.player.state.duration > 0 ? deps.player.state.duration : duration;
  }

  const on = {
    play() {
      sync();
      if (playing) return;
      playing = true;
      completed = false;
      lastPos = pos();
      duration = dur();
      started = true;
      emit("start", { from_ms: ms(lastPos) });
      segStart = lastPos;
    },
    pause() {
      sync();
      lastPos = pos();
      playing = false;
      closeSegment(lastPos);
    },
    timeupdate() {
      sync();
      lastPos = pos();
      duration = dur();
    },
    seeked() {
      sync();
      const to = pos();
      const from = lastPos;
      if (!started || Math.abs(to - from) < MIN_SEEK_S) {
        lastPos = to;
        return;
      }
      closeSegment(from);
      emit("seek", { from_ms: ms(from), to_ms: ms(to) });
      lastPos = to;
      if (playing) segStart = to;
    },
    ended() {
      // The player has usually advanced to the next track already: finish the one we were recording.
      const end = dur() > 0 ? dur() : pos();
      lastPos = end;
      playing = false;
      closeSegment(end);
      if (started && !completed) {
        completed = true;
        emit("complete", { to_ms: ms(end) });
      }
    },
    loadedmetadata() {
      duration = dur();
    },
  };

  const offs: (() => void)[] = [];
  if (el) {
    for (const [type, fn] of Object.entries(on)) {
      el.addEventListener(type, fn);
      offs.push(() => el.removeEventListener(type, fn));
    }
  }

  return {
    sync,
    /** Close the open segment and reopen it at the same place (visibility hidden). */
    onHidden() {
      sync();
      if (segStart === null) return;
      lastPos = pos();
      closeSegment(lastPos);
      if (playing) segStart = lastPos;
    },
    /** 30 s checkpoint. */
    tick() {
      this.onHidden();
    },
    destroy() {
      offs.forEach((f) => f());
      offs.length = 0;
    },
  };
}

/** Wires the recorder to the app-wide player and the outbox. Call from a component effect; returns cleanup. */
export function installListen(player: Player): () => void {
  const el = player.el;
  if (!el) return () => {};
  const rec = createRecorder({
    player,
    emit: (e) => void outbox.append(e),
    sessionId: tabSession(),
    deviceId: () => {
      try {
        return getDevice().deviceId;
      } catch {
        return undefined;
      }
    },
    id: newEventId,
    // eslint-disable-next-line svelte/prefer-svelte-reactivity -- a timestamp, not state
    now: () => new Date(),
  });
  const stopFx = $effect.root(() => {
    $effect(() => {
      void player.state.trackId;
      untrack(() => rec.sync());
    });
  });
  const vis = () => {
    if (document.visibilityState === "hidden") rec.onHidden();
  };
  // Registered before the outbox's own hide listener so the closed segment is queued before the beacon.
  document.addEventListener("visibilitychange", vis);
  const hide = () => rec.onHidden();
  window.addEventListener("pagehide", hide);
  const stopOutbox = startOutbox();
  const timer = setInterval(() => rec.tick(), TICK_MS);
  return () => {
    clearInterval(timer);
    document.removeEventListener("visibilitychange", vis);
    window.removeEventListener("pagehide", hide);
    stopFx();
    rec.destroy();
    stopOutbox();
  };
}

let session = "";
function tabSession(): string {
  session ||= uuidv7();
  return session;
}

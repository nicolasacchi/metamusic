// Audio focus lease (audit 4.4): a duel (or any A/B listener) borrows the single output from the player and gives it
// back. Plain TypeScript, no DOM: the Player supplies the host.
//
//   acquire(owner)    pauses the player without clearing what the user wanted (a lease is not a user pause)
//   release(resume)   gives it back: resumes at once when still inside the click gesture, else leaves a
//                     "Resume <title>" hold in the mini-player. Never resumes on a timer.
//   interrupt()       any user transport action on the player (play, next, prev, toggle, pause) ends the lease
//                     for good: nothing is resumed behind the user's back.

/** What a lease owner lets the lock screen do while it holds focus (Media Session play/pause). */
export interface LeaseHandlers {
  play(): void;
  pause(): void;
  playing(): boolean;
}

export interface LeaseOptions {
  handlers?: LeaseHandlers;
  /** Runs once when the lease ends, however it ends (the duel suspends the shared AudioContext here). */
  onEnd?(): void;
}

export interface ReleaseOptions {
  /** Give the audio back (resume the track that was playing). Default false. */
  resume?: boolean;
  /** The caller knows whether it runs inside a user gesture; otherwise the host decides. */
  gesture?: boolean;
}

export interface Lease {
  readonly owner: string;
  /** false once released or interrupted. */
  readonly active: boolean;
  setHandlers(h: LeaseHandlers | undefined): void;
  /** Tell the host the owner's play state changed (Media Session playbackState). */
  changed(): void;
  release(o?: ReleaseOptions): void;
}

export interface FocusHost {
  /** Playback intent right now (true: the user is listening). */
  wasPlaying(): boolean;
  trackId(): string;
  /** Pause the output without touching the playback intent bookkeeping the Player keeps for a user pause. */
  suspend(): void;
  /** User-style resume; only called inside a gesture. */
  resume(): void;
  /** Show or hide the "Resume <title>" affordance. */
  hold(on: boolean): void;
  inGesture(): boolean;
  changed(): void;
}

export interface AudioFocus {
  acquire(owner: string, o?: LeaseOptions): Lease;
  current(): Lease | null;
  handlers(): LeaseHandlers | null;
  interrupt(): void;
}

interface Slot {
  wasPlaying: boolean;
  trackId: string;
}

export function createAudioFocus(host: FocusHost): AudioFocus {
  let cur: (Lease & { end(): void }) | null = null;
  let slot: Slot | null = null;
  let handlers: LeaseHandlers | undefined;

  function make(owner: string, o: LeaseOptions): Lease & { end(): void } {
    let active = true;
    let onEnd = o.onEnd;
    const lease = {
      owner,
      get active() {
        return active;
      },
      setHandlers(h: LeaseHandlers | undefined) {
        if (active) handlers = h;
      },
      changed() {
        if (active) host.changed();
      },
      end() {
        if (!active) return;
        active = false;
        if (cur === lease) {
          cur = null;
          handlers = undefined;
        }
        const f = onEnd;
        onEnd = undefined;
        f?.();
      },
      release(r: ReleaseOptions = {}) {
        if (!active) return;
        const s = slot;
        lease.end();
        slot = null;
        const back = !!r.resume && !!s && s.wasPlaying && s.trackId !== "" && s.trackId === host.trackId();
        if (!back) {
          host.hold(false);
          host.changed();
          return;
        }
        if (r.gesture ?? host.inGesture()) {
          host.hold(false);
          host.resume();
        } else {
          host.hold(true);
          host.changed();
        }
      },
    };
    return lease;
  }

  return {
    acquire(owner, o = {}) {
      if (cur && cur.owner === owner) {
        if (o.handlers) handlers = o.handlers;
        return cur;
      }
      // Another owner takes over: it inherits what the first one found, so a duel -> compare hop still returns the music.
      const inherited = slot;
      if (cur) {
        const old = cur;
        old.end();
      }
      slot = inherited ?? { wasPlaying: host.wasPlaying(), trackId: host.trackId() };
      host.hold(false);
      host.suspend();
      const lease = make(owner, o);
      cur = lease;
      handlers = o.handlers;
      host.changed();
      return lease;
    },
    current: () => cur,
    handlers: () => (cur ? (handlers ?? null) : null),
    interrupt() {
      if (!cur) return;
      const l = cur;
      slot = null;
      l.end();
      host.hold(false);
      host.changed();
    },
  };
}

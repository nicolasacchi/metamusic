// Timestamp jumps (C40.2): comment timestamps and waveform pins all go through jumpTo.
import { player as appPlayer } from "./player.svelte";
import type { PlayOptions } from "./player.svelte";
import { pushToast } from "./toast.svelte";

export interface JumpPlayer {
  state: { trackId: string; duration: number; current?: number };
  title?: string;
  el: { paused: boolean } | null;
  seek(s: number): void;
  resume(): void;
  play(trackId: string, queue?: string[], opts?: PlayOptions): void;
  playNext?(trackId: string, quiet?: boolean): void;
  next?(startAt?: number): void;
}

export interface JumpDeps {
  player: JumpPlayer;
  navigate(to: string): void;
  /** "Back to <title>" offer after a jump away from the track that was loaded. */
  offerBack?(title: string, back: () => void): void;
}

function defaultOfferBack(title: string, back: () => void) {
  pushToast("Jumped to another take", { kind: "info", ms: 8000, action: { label: `Back to ${title}`, run: back } });
}

/** Client-side navigation without a router handle: the router listens to popstate. */
function defaultNavigate(to: string) {
  history.pushState(null, "", to);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

export function jumpTo(trackId: string, seconds: number, deps: JumpDeps = { player: appPlayer, navigate: defaultNavigate }) {
  const { player, navigate } = deps;
  const offerBack = deps.offerBack ?? defaultOfferBack;
  let t = Number.isFinite(seconds) ? Math.max(0, seconds) : 0;
  if (player.state.trackId === trackId) {
    if (player.state.duration > 0) t = Math.min(t, player.state.duration);
    player.seek(t);
    if (player.el?.paused) player.resume();
    return;
  }
  // Must run inside the click's user gesture. The Track page's ?t= handling then finds the track already
  // current and only re-seeks.
  const prevId = player.state.trackId;
  if (prevId && player.playNext && player.next) {
    // Something is loaded: the queue is kept. The take plays next and is started now, so "previous" and the
    // toast's "Back" return to where the listener was.
    const prevTitle = player.title || "the previous take";
    const prevAt = player.state.current ?? 0;
    player.playNext(trackId, true);
    player.next(t);
    offerBack(prevTitle, () => player.play(prevId, undefined, { startAt: prevAt, title: prevTitle }));
  } else {
    player.play(trackId, undefined, { startAt: t });
  }
  navigate(`/tracks/${encodeURIComponent(trackId)}?t=${t}`);
}

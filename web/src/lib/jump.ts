// Timestamp jumps (C40.2): comment timestamps and waveform pins all go through jumpTo.
import { player as appPlayer } from "./player.svelte";
import type { PlayOptions } from "./player.svelte";

export interface JumpPlayer {
  state: { trackId: string; duration: number };
  el: { paused: boolean } | null;
  seek(s: number): void;
  resume(): void;
  play(trackId: string, queue?: string[], opts?: PlayOptions): void;
}

export interface JumpDeps {
  player: JumpPlayer;
  navigate(to: string): void;
}

/** Client-side navigation without a router handle: the router listens to popstate. */
function defaultNavigate(to: string) {
  history.pushState(null, "", to);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

export function jumpTo(trackId: string, seconds: number, deps: JumpDeps = { player: appPlayer, navigate: defaultNavigate }) {
  const { player, navigate } = deps;
  let t = Number.isFinite(seconds) ? Math.max(0, seconds) : 0;
  if (player.state.trackId === trackId) {
    if (player.state.duration > 0) t = Math.min(t, player.state.duration);
    player.seek(t);
    if (player.el?.paused) player.resume();
    return;
  }
  // play() first: it must run inside the click's user gesture. The Track page's ?t= handling
  // then finds the track already current and only re-seeks.
  player.play(trackId, undefined, { startAt: t });
  navigate(`/tracks/${encodeURIComponent(trackId)}?t=${t}`);
}

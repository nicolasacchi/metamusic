/** The player counts as "in use" (a reload would lose it) when anything is playing, waiting for a tap, loaded or queued. */
export interface PlayerUseState {
  playing: boolean;
  pendingPlay: boolean;
  trackId: string;
  queue: readonly string[];
}

export function playerInUse(s: PlayerUseState): boolean {
  return s.playing || s.pendingPlay || s.trackId !== "" || s.queue.length > 0;
}

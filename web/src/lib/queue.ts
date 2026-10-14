// Queue semantics for entry points that are not a list (the track page).
//
// Playing from a list queues the visible order (Library, Playlist). The track page has no list, so it
// either keeps the queue the track is already part of (the user navigated there from a list) or queues
// the track followed by the other takes of its song group (lineage siblings, in the order shown), so the
// next take starts by itself instead of the player stopping silently after one track.

export function trackPageQueue(id: string, current: readonly string[], siblings: readonly { id: string }[]): string[] | undefined {
  if (current.includes(id)) return undefined;
  const seen = new Set([id]);
  const rest: string[] = [];
  for (const s of siblings) {
    if (seen.has(s.id)) continue;
    seen.add(s.id);
    rest.push(s.id);
  }
  return [id, ...rest];
}

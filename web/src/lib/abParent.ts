// "A/B with parent": a non-blind duel of the parent against the child (M2b), one idempotency key per click.
import { api } from "../api/client";
import { newIdempotencyKey } from "../api/idem";
import { createDuel } from "./duelApi";

/** Creates the duel and navigates to it; resolves to an error message, or "" on success. */
export async function openAbWithParent(parentId: string, trackId: string, navigate: (to: string) => void): Promise<string> {
  try {
    const d = await createDuel(api, parentId, trackId, newIdempotencyKey(), { blind: false, matchLoudness: true });
    navigate(`/duels/${encodeURIComponent(d.id)}`);
    return "";
  } catch (e) {
    return e instanceof Error ? e.message : "Could not create the duel";
  }
}

/** Router-less navigation (the router listens to popstate). */
export function pushNavigate(to: string): void {
  history.pushState(null, "", to);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

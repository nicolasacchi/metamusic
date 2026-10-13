// Typed M2c fixtures (synthetic values only): a known, a reference (lossless, recipe-less) and a lossy
// reference track as library rows and detail pages, the `flac: null` media links of a lossy track, and
// blind/revealed duels between two lossy siblings. Hand-maintained JSON; `svelte-check` types them.
import type { components } from "../../../api/schema";
import duelOpenBlindLossyJson from "./duel_open_blind_lossy.json";
import duelRevealedLossyJson from "./duel_revealed_lossy.json";
import mediaLinksLossyJson from "./media_links_lossy.json";
import summaryKnownJson from "./summary_known.json";
import summaryLossyJson from "./summary_lossy.json";
import summaryReferenceJson from "./summary_reference.json";
import trackKnownJson from "./track_known.json";
import trackLossyJson from "./track_lossy.json";
import trackReferenceJson from "./track_reference.json";

type S = components["schemas"];

export const summaryKnown = summaryKnownJson as unknown as S["TrackSummary"];
export const summaryReference = summaryReferenceJson as unknown as S["TrackSummary"];
export const summaryLossy = summaryLossyJson as unknown as S["TrackSummary"];
export const trackKnown = trackKnownJson as unknown as S["TrackDetail"];
export const trackReference = trackReferenceJson as unknown as S["TrackDetail"];
export const trackLossy = trackLossyJson as unknown as S["TrackDetail"];
/** A lossy track's media links: `flac` is null and `mp3` is the master itself. */
export const mediaLinksLossy = mediaLinksLossyJson as unknown as S["MediaLinksOutBody"];
/** An open blind duel between two lossy siblings: the tracks (and so both chips) are hidden. */
export const duelOpenBlindLossy = duelOpenBlindLossyJson as unknown as S["Duel"];
export const duelRevealedLossy = duelRevealedLossyJson as unknown as S["Duel"];

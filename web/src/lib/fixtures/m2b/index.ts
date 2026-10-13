// Typed M2b response fixtures. The JSON files are written by
// `go test ./internal/api -run TestWebFixtures_M2b -update` from typed Go values and validated
// against the OpenAPI response schemas there; synthetic values only.
import type { components } from "../../../api/schema";
import comparisonJson from "./comparison.json";
import dossierJson from "./dossier.json";
import duelOpenBlindJson from "./duel_open_blind.json";
import duelRevealedJson from "./duel_revealed.json";
import ladderPageJson from "./ladder_page.json";
import spectrogramViewJson from "./spectrogram_view.json";

type S = components["schemas"];

/** An open blind duel: tracks, gains and loudness are null until the human verdict exists. */
export const duelOpenBlind = duelOpenBlindJson as unknown as S["Duel"];
/** A decided duel as the operator sees it: tracks revealed, own and critic verdicts present. */
export const duelRevealed = duelRevealedJson as unknown as S["Duel"];
export const ladderPage = ladderPageJson as unknown as S["LadderPage"];
export const dossier = dossierJson as unknown as S["Dossier"];
/** A spectrogram view without the inline image (inline=false). */
export const spectrogramView = spectrogramViewJson as unknown as S["SpectrogramView"];
export const comparison = comparisonJson as unknown as S["Comparison"];

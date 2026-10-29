// Copy and shape of the two curation opt-ins (C131.4). Both default off; only a human may change them.
import type { components } from "../api/schema";

export type CurationSettings = components["schemas"]["CurationSettings"];
export type CurationKey = "export_muse_names" | "subsonic_muse_names";

export interface ToggleInfo {
  key: CurationKey;
  label: string;
  /** What the setting does, in plain words, for both positions. */
  off: string;
  on: string;
  /** Shown while the effect cannot be seen yet. */
  note?: string;
}

export const TOGGLES: readonly ToggleInfo[] = [
  {
    key: "export_muse_names",
    label: "Name inspirations in tape files",
    off: "Off: Subsonic apps and tape files never contain an inspiration's name.",
    on: "On: a tape's liner notes may name the inspiration (Muse) behind a Song.",
  },
  {
    key: "subsonic_muse_names",
    label: "Name inspirations in Subsonic groupings",
    off: "Off: automatic groupings in Subsonic apps get neutral names (Inspiration 001, 002, ...).",
    on: "On: automatic groupings are named after their inspiration.",
    note: "No effect until automatic albums are served to Subsonic apps.",
  },
];

/** The only body ever sent: the changed key alone. */
export function patchFor(key: CurationKey, value: boolean): Partial<Record<CurationKey, boolean>> {
  return { [key]: value };
}

export const valueOf = (s: Pick<CurationSettings, CurationKey>, key: CurationKey): boolean => s[key];

// The Sounds page view model (C100.5, C100.6): facet selection <-> `sound=` params, facet rendering order and
// the "Map…" form validation (C93.2). Pure functions; the page owns the state and the network.

/** The seven facets in display order (C93). */
export const FACETS = ["genre_family", "era", "tempo_band", "vocal", "mood", "language", "engine"] as const;
export type Facet = (typeof FACETS)[number];
/** Term-derived facets: the only ones `map_sound_term` accepts (D-L1-15; `tempo_band` and `engine` are computed). */
export const MAPPABLE_FACETS = ["genre_family", "era", "vocal", "mood", "language"] as const;
export type MappableFacet = (typeof MAPPABLE_FACETS)[number];

export const FACET_LABELS: Record<Facet, string> = {
  genre_family: "Genre",
  era: "Era",
  tempo_band: "Tempo",
  vocal: "Vocal",
  mood: "Mood",
  language: "Language",
  engine: "Engine",
};

export type Tab = "songs" | "terms";
export interface SoundsState {
  /** `facet:value` selections (OR inside a facet, AND across facets). */
  sounds: string[];
  tempoMismatch: boolean;
  tab: Tab;
}

const isFacet = (s: string): s is Facet => (FACETS as readonly string[]).includes(s);

export function parseSound(s: string): { facet: Facet; value: string } | null {
  const i = s.indexOf(":");
  if (i <= 0) return null;
  const facet = s.slice(0, i);
  const value = s.slice(i + 1);
  return isFacet(facet) && value !== "" ? { facet, value } : null;
}

export function toggleSound(sounds: readonly string[], facet: string, value: string): string[] {
  const key = `${facet}:${value}`;
  return sounds.includes(key) ? sounds.filter((s) => s !== key) : [...sounds, key];
}

export function soundsFromSearch(search: string): SoundsState {
  const p = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const sounds = [...new Set(p.getAll("sound"))].filter((s) => parseSound(s) !== null);
  return { sounds, tempoMismatch: p.get("tempo_mismatch") === "true", tab: p.get("tab") === "terms" ? "terms" : "songs" };
}

/** Query string (no leading `?`) for the page URL; defaults omitted. */
export function soundsToSearch(s: SoundsState): string {
  const p = new URLSearchParams();
  for (const x of s.sounds) p.append("sound", x);
  if (s.tempoMismatch) p.set("tempo_mismatch", "true");
  if (s.tab !== "songs") p.set("tab", s.tab);
  return p.toString();
}

export interface SoundsQuery {
  sound?: string[];
  tempo_mismatch?: boolean;
  limit?: number;
  cursor?: string;
}
/** The search_sounds query for this state (defaults omitted so the server applies its own). */
export function soundsToQuery(s: SoundsState, o: { limit?: number; cursor?: string } = {}): SoundsQuery {
  const q: SoundsQuery = {};
  if (s.sounds.length) q.sound = [...s.sounds];
  if (s.tempoMismatch) q.tempo_mismatch = true;
  if (o.limit !== undefined) q.limit = o.limit;
  if (o.cursor) q.cursor = o.cursor;
  return q;
}

export interface Pill {
  key: string;
  facet: Facet;
  value: string;
  label: string;
}
/** Selected pills in facet order, then in selection order. */
export function selectedPills(sounds: readonly string[]): Pill[] {
  const out: Pill[] = [];
  for (const key of sounds) {
    const p = parseSound(key);
    if (p) out.push({ key, facet: p.facet, value: p.value, label: `${FACET_LABELS[p.facet]}: ${p.value}` });
  }
  return out.sort((a, b) => FACETS.indexOf(a.facet) - FACETS.indexOf(b.facet));
}

interface Count {
  value: string;
  count: number;
  mismatches?: number;
}
export interface FacetBlock {
  facet: Facet;
  label: string;
  values: Count[];
}
/** Every facet in the fixed order (empty ones kept); values by count desc, then value. Unknown facets are dropped. */
export function orderedFacets(counts: Record<string, readonly Count[] | null | undefined>): FacetBlock[] {
  return FACETS.map((facet) => ({
    facet,
    label: FACET_LABELS[facet],
    values: [...(counts[facet] ?? [])].sort((a, b) => b.count - a.count || (a.value < b.value ? -1 : a.value > b.value ? 1 : 0)),
  }));
}

const ERA = /^(5|6|7|8|9|0|1|2)0s$/;
const LANG = /^[a-z]{2}$/;
const SLUG = /^[a-z][a-z0-9-]{0,39}$/;
export const MAX_TERM_LEN = 60;

export function mappingOptions(): { facet: MappableFacet; label: string; hint: string }[] {
  const hint: Record<MappableFacet, string> = {
    genre_family: "lower-case slug, e.g. hip-hop",
    era: "decade, e.g. 90s",
    vocal: "lower-case slug, e.g. female",
    mood: "lower-case slug, e.g. dreamy",
    language: "two letters, e.g. it",
  };
  return MAPPABLE_FACETS.map((facet) => ({ facet, label: FACET_LABELS[facet], hint: hint[facet] }));
}

/** "" when valid; otherwise the message to show. `term` is validated only when given. */
export function validateMapping(m: { facet: string; value: string; term?: string }): string {
  if (!(MAPPABLE_FACETS as readonly string[]).includes(m.facet)) return "This facet is computed and cannot be mapped.";
  if (m.term !== undefined) {
    const t = m.term.trim();
    if (t === "" || !/\p{L}/u.test(t)) return "The term needs at least one letter.";
    if ([...t].length > MAX_TERM_LEN) return `The term is at most ${MAX_TERM_LEN} characters.`;
  }
  if (m.facet === "era") return ERA.test(m.value) ? "" : "Era looks like 50s, 90s, 00s or 20s.";
  if (m.facet === "language") return LANG.test(m.value) ? "" : "Language is two lower-case letters.";
  return SLUG.test(m.value) ? "" : "Use a lower-case slug (letters, digits, hyphen; at most 40 characters).";
}

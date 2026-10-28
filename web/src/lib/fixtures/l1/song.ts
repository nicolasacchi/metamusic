// Synthetic Song and Muse page fixtures (no real data).
import type { components } from "../../../api/schema";

type S = components["schemas"];

const actor = { kind: "human", id: "u1", name: "operator" } as unknown as S["PrincipalRef"];
const card = (over: Partial<S["TakeCard"]> & { id: string }) => ({
  title: `Synthetic take ${over.id}`,
  status: "draft",
  generation: 0,
  parent_track_id: null,
  origin: "generated",
  duration_s: 120,
  recorded_at: "2026-01-02T10:00:00.000Z",
  lossy: false,
  recipe_known: true,
  stars: null,
  critic_stars: null,
  is_pick: false,
  pinned: false,
  has_ears: false,
  flags: [],
  ladder: null,
  tempo: null,
  cost_display_usd: null,
  ...over,
}) as S["TakeCard"];

export const songView = {
  song: {
    id: "s1",
    title: "Synthetic song one",
    angle: "acoustic",
    angle_source: "manual",
    origin: "root",
    revision: 3,
    state: "picked",
    hidden: false,
    archived: false,
    queued_jobs: 0,
    created_at: "2026-01-01T00:00:00.000Z",
    updated_at: "2026-01-02T00:00:00.000Z",
    root_track_id: "t-root",
    root_track_ids: ["t-root"],
    draft_id: null,
    retired: false,
    retired_into: null,
  },
  muse: { id: "muse-a", kind: "reference", title: "Synthetic muse A", artist: "Synthetic artist" },
  draft: null,
  takes: [
    card({ id: "t-old", recorded_at: "2026-01-01T10:00:00.000Z", stars: 3, lossy: true, recipe_known: false }),
    card({ id: "t-bad", status: "discarded", recorded_at: "2026-01-01T11:00:00.000Z", flags: ["clipping"] }),
    card({ id: "t-pick", recorded_at: "2025-12-01T10:00:00.000Z", is_pick: true, pinned: true, stars: 5, ladder: { u: 1.2, sd: 0.55, n_duels: 4 }, has_ears: true, tempo: { requested_bpm: 76, measured_bpm: 75 } }),
  ],
  takes_total: 3,
  pick: {
    changed_at: "2026-01-02T00:00:00.000Z",
    evidence: { rule: "pinned" },
    last_event_id: "e2",
    open_proposals: 1,
    pending: false,
    pin_ineligible: false,
    pinned_at: "2026-01-02T00:00:00.000Z",
    pinned_by: actor,
    pinned_take_id: "t-pick",
    seq: 2,
    source: "pinned",
    take_id: "t-pick",
  },
  proposals: [{ id: "pp1", song_id: "s1", take_id: "t-old", proposed_by: { kind: "agent", id: "a1", name: "agent" }, rationale: "Synthetic rationale", state: "open", created_at: "2026-01-02T00:00:00.000Z", decided_at: null, decided_by: null }],
  events: [
    { id: "e2", song_id: "s1", seq: 2, cursor: 2, at: "2026-01-02T00:00:00.000Z", actor, reason: "pin", from_source: "newest", from_take_id: "t-old", to_source: "pinned", to_take_id: "t-pick", evidence: {} },
    { id: "e1", song_id: "s1", seq: 1, cursor: 1, at: "2026-01-01T00:00:00.000Z", actor, reason: "initial", from_source: "none", from_take_id: null, to_source: "newest", to_take_id: "t-old", evidence: {} },
  ],
  sounds: {
    taxonomy_version: 1,
    facets: { era: [{ value: "90s", origin: "agent" }] },
    tempo: { basis: "measured", measured_bpm: 75, mismatch: false, octave: false, plan_bpm: null, requested_bpm: 76 },
    unmapped: [],
  },
  sessions: [{ id: "ses1", kind: "import", name: "Synthetic session" }],
  lineage: { root_track_id: "t-root", generations: 2, children_total: 1 },
  pending: false,
} as unknown as S["SongView"];

const sum = (id: string, title: string) => ({
  id,
  title,
  angle: "acoustic",
  archived: false,
  draft_id: null,
  hidden: false,
  muse_id: "muse-a",
  open_proposals: 0,
  pick: null,
  queued_jobs: 0,
  root_track_id: null,
  state: "rendered",
  takes: 2,
  updated_at: "2026-01-02T00:00:00.000Z",
}) as unknown as S["SongSummary"];

export const museDetail: S["MuseDetail"] = {
  muse: { id: "muse-a", kind: "reference", title: "Synthetic muse A", artist: "Synthetic artist", brief: null, created_by: "human", hidden: false, revision: 2, source: "human", updated_at: "2026-01-02T00:00:00.000Z" },
  coverage: { songs: 2, draft: 0, rendered: 2, judged: 0, picked: 0, queued_jobs: 0 },
  angles: [{ angle: "acoustic", songs: 2 }],
  songs: [sum("s1", "Synthetic song one"), sum("s2", "Synthetic song two")],
  songs_total: 2,
  catalog: null,
};

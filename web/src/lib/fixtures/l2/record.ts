// Synthetic Record fixtures for the L2 Records pages (no real titles, no Muse text).
import type { components } from "../../../api/schema";

type S = components["schemas"];

export function slot(n: number, over: Partial<S["SlotView"]> = {}): S["SlotView"] {
  return {
    id: `sl-${n}`,
    position: n,
    side: null,
    mode: "floating",
    state: "ok",
    song: { id: `s-${n}`, title: `Synthetic song ${n}`, angle: null, state: "picked", retired: false, retired_into: null },
    take: { id: `t-${n}`, title: `Synthetic take ${n}`, status: "draft", duration_s: 180 + n, lossy: false, recipe_known: true, stars: null },
    pick: { take_id: `t-${n}`, source: "ladder" },
    pin_differs: false,
    gain_db: -1.5,
    why: "won 3 blind duels",
    warnings: [],
    ...over,
  };
}

export const cover: S["CoverLinks"] = { url_512: "/media/c512.png", url_1024: "/media/c1024.png", expires_at: "2099-01-01T00:00:00Z" };

export function recordView(n = 3, over: Partial<S["RecordView"]> = {}): S["RecordView"] {
  const slots = Array.from({ length: n }, (_, i) => slot(i + 1));
  return {
    record: {
      id: "rec-1",
      title: "Synthetic record",
      subtitle: "A synthetic subtitle",
      liner_note: "",
      status: "draft",
      revision: 4,
      cover_rerolls: 0,
      hidden: false,
      created_by: { id: "human:h", kind: "human", name: "h" },
      created_at: "2026-10-15T12:00:00Z",
      updated_at: "2026-10-15T12:00:00Z",
      proposal: null,
      published: null,
    },
    slots,
    totals: { slots: n, playable: n, duration_s: slots.reduce((a, s) => a + (s.take?.duration_s ?? 0), 0), lossy: 0, warnings: 0, undecided: 0 },
    warnings: [],
    cover,
    pending: false,
    ...over,
  };
}

export const preview: S["RecordPreview"] = {
  chapters: [
    { position: 1, name: "Synthetic take 1" },
    { position: 2, name: "Synthetic take 2 (lossy source)" },
  ],
  tags: [
    { key: "TITLE", value: "Synthetic record" },
    { key: "ARTIST", value: "Synthetic artist" },
  ],
  description: "1. Synthetic song 1\n2. Synthetic song 2",
  warnings: [{ slot_id: "sl-2", code: "undecided_pick" }],
};

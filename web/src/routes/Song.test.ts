import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Song from "./Song.svelte";
import Muse from "./Muse.svelte";
import TempoBadge from "../lib/components/TempoBadge.svelte";
import { songView, museDetail } from "../lib/fixtures/l1/song";

const human = { kind: "human", scopes: ["read", "write", "generate"] };
const page = (view = songView, v: unknown = human) => render(Song, { props: { id: view.song.id, initial: view, viewer: v, navigate: () => {} } } as never).body;

describe("Song page", () => {
  const h = page();
  test("header, Muse link, sessions and tempo badge", () => {
    expect(h).toContain("Synthetic song one");
    expect(h).toContain('href="/muses/muse-a"');
    expect(h).toContain("requested 76 · measured 75 ✓");
    expect(h).toContain("Synthetic session");
  });
  test("take cards: Pick first with badge, ladder, badges and flags", () => {
    expect((h.match(/data-testid="take-card"/g) ?? []).length).toBe(3);
    expect(h.indexOf('data-take-id="t-pick"')).toBeLessThan(h.indexOf('data-take-id="t-old"'));
    expect(h).toContain('data-testid="pick-badge"');
    expect(h).toContain("u 1.20 ± 0.55 · 4 duels");
    expect(h).toContain("Lossy MP3");
    expect(h).toContain("Recipe unknown");
    expect(h).toContain("clipping");
  });
  test("pin controls: pin, unpin, none for a discarded take", () => {
    const card = (id: string) => h.slice(h.indexOf(`data-take-id="${id}"`)).split('data-testid="take-card"')[0]!;
    expect(card("t-pick")).toContain('data-testid="unpin"');
    expect(card("t-old")).toContain('data-testid="pin"');
    expect(card("t-bad")).not.toContain('data-testid="pin"');
    expect(card("t-bad")).not.toContain('data-testid="unpin"');
  });
  test("proposals offer Accept and Dismiss to a human; history and lineage link render", () => {
    expect(h).toContain('data-testid="proposal"');
    expect(h).toContain("Synthetic rationale");
    expect(h).toContain('data-testid="proposal-accept"');
    expect(h).toContain('data-testid="proposal-dismiss"');
    expect(h).toContain('data-testid="pick-history"');
    expect(h).toContain('href="/lineage/t-root"');
  });
  test("an agent viewer gets no pin/accept controls", () => {
    const a = page(songView, { kind: "agent", scopes: ["read", "write"] });
    expect(a).not.toContain('data-testid="pin"');
    expect(a).not.toContain('data-testid="proposal-accept"');
  });
  test("a draft Song shows the draft summary and Generate", () => {
    const d = page({ ...songView, takes: [], takes_total: 0, pick: null, proposals: [], events: [], draft: { id: "d1", status: "draft", revision: 1, label: "Synthetic draft", request_id: "r", group_key: null } } as never);
    expect(d).toContain("Synthetic draft");
    expect(d).toContain('data-testid="song-generate"');
    expect(d).not.toContain('data-testid="take-card"');
  });
  test("a retired Song says so", () => {
    const r = page({ ...songView, song: { ...songView.song, retired: true, retired_into: "s-new" }, takes: [], takes_total: 0, pick: null, proposals: [], events: [] } as never);
    expect(r).toContain('href="/songs/s-new"');
  });
});

describe("TempoBadge", () => {
  const r = (tempo: unknown) => render(TempoBadge, { props: { tempo } } as never).body;
  test("renders the text and nothing for none", () => {
    expect(r({ requested_bpm: 76, measured_bpm: 152, mismatch: true, octave: true })).toContain("≈");
    expect(r(null)).not.toContain("tempo-badge");
  });
});

describe("Muse page", () => {
  const h = render(Muse, { props: { id: "muse-a", initial: museDetail, viewer: human, navigate: () => {} } } as never).body;
  test("header, coverage, angles, chips and the Board link", () => {
    expect(h).toContain("Synthetic muse A");
    expect(h).toContain('data-testid="muse-coverage"');
    expect(h).toContain("acoustic");
    expect((h.match(/data-testid="song-chip"/g) ?? []).length).toBe(2);
    expect(h).toContain('href="/board?muse=muse-a"');
  });
});

import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Track from "./Track.svelte";
import { trackKnown, trackLossy, trackReference } from "../lib/fixtures/m2c";

const page = (t: typeof trackKnown, viewer?: unknown) => render(Track, { props: { id: t.id, initial: t, ...(viewer ? { viewer } : {}) } } as never).body;

describe("Track page (M2c)", () => {
  test("a reference track shows the Recipe unknown panel and no Lyrics or Recipe panels", () => {
    const h = page(trackReference);
    expect(h).toContain("Recipe unknown");
    expect(h).toContain('data-testid="recipe-unknown"');
    expect(h).not.toMatch(/<h2[^>]*>Lyrics<\/h2>/);
    expect(h).not.toMatch(/<h2[^>]*>Recipe<\/h2>/);
    expect(h).not.toContain('data-testid="recipe-style"');
    expect(h).not.toContain("Lossy MP3");
  });

  test("a lossy track shows Lossy MP3 and the bitrate", () => {
    const h = page(trackLossy);
    expect(h).toContain("Lossy MP3");
    expect(h).toMatch(/no lossless master/);
    expect(h).toMatch(/\b320 kbps\b/);
    expect(h).not.toMatch(/<h2[^>]*>Lyrics<\/h2>/);
  });

  test("a known track is unchanged", () => {
    const h = page(trackKnown);
    expect(h).toMatch(/<h2[^>]*>Lyrics<\/h2>/);
    expect(h).toMatch(/<h2[^>]*>Recipe<\/h2>/);
    expect(h).toContain('data-testid="recipe-style"');
    expect(h).not.toContain("Recipe unknown");
    expect(h).not.toContain("Lossy MP3");
    expect(h).not.toContain("Recover recipe");
  });
});

describe("Track page (M4a W2)", () => {
  test("shows cover, download, offline and prompt actions", () => {
    const h = page(trackKnown);
    expect(h).toContain('data-testid="cover-placeholder"');
    expect(h).toContain('data-testid="track-actions"');
    expect(h).toContain('data-testid="copy-critique-prompt"');
    expect(h).toContain('data-testid="copy-variant-prompt"');
  });

  test("the variant prompt is disabled when the recipe is unknown", () => {
    const h = page(trackReference);
    expect(h).toMatch(/data-testid="copy-variant-prompt"[^>]*disabled|disabled[^>]*data-testid="copy-variant-prompt"/);
    expect(h).not.toMatch(/data-testid="copy-critique-prompt"[^>]*disabled/);
  });
});

describe("Track page Song strip (L1)", () => {
  const song = { id: "s_1", title: "Synthetic Song", angle: null, muse_id: null, pick_source: "ladder" };
  test("shows the Song link and the Pick badge when the take is the Pick", () => {
    const h = page({ ...trackKnown, song, is_pick: true } as never);
    expect(h).toContain('data-testid="song-strip"');
    expect(h).toContain('href="/songs/s_1"');
    expect(h).toContain("Synthetic Song");
    expect(h).toContain('data-testid="pick-badge"');
    expect(h).not.toContain("Pick of this Song");
  });
  test("links to the Pick of the Song when the take is not the Pick", () => {
    const h = page({ ...trackKnown, song, is_pick: false } as never);
    expect(h).toContain("Pick of this Song");
    expect(h).toContain('href="/songs/s_1"');
    expect(h).not.toContain('data-testid="pick-badge"');
  });
  test("no strip without a Song", () => {
    expect(page({ ...trackKnown, song: null } as never)).not.toContain('data-testid="song-strip"');
    expect(page(trackKnown)).not.toContain('data-testid="song-strip"');
  });
});

describe("Track page curation entry points (L2)", () => {
  test("Add to Record and the Take Crate menu are on the page", () => {
    const h = page(trackKnown);
    expect(h).toContain('data-testid="track-curation"');
    expect(h).toContain('data-testid="add-to-record"');
    expect(h).toContain('data-testid="add-to-crate"');
    expect(h).toContain("Take Crates");
  });
});

describe("Track page (T1 W1b)", () => {
  test("a transform output shows the origin chip and the Transform provenance", () => {
    const t = { ...trackKnown, origin: "transform", transform: { kind: "glacier", spec_hash: "0123456789abcdef", transform_id: trackKnown.id } };
    const h = page(t as unknown as typeof trackKnown);
    expect(h).toContain('data-testid="origin-chip"');
    expect(h).toContain("dsp · Glacier");
    expect(h).toContain("01234567");
  });
  test("an ordinary take shows neither", () => {
    const h = page(trackKnown);
    expect(h).not.toContain('data-testid="origin-chip"');
    expect(h).not.toContain('data-testid="transform-provenance"');
  });
});

// U1-W2: shells per role. The Owner's page is pinned (every panel present); an Editor and a Listener lose what they cannot use.
const withAllowed = (allowed: string[]) => ({ kind: "human", scopes: [], allowed });
const OWNER_OPS = ["propose_patch", "list_comments", "create_voice_note", "update_playlist", "get_track_context", "set_tags", "edit_crate_items", "edit_record_slots", "create_transform", "create_generation_human"];
const LISTENER_OPS = ["get_track", "rate_track", "list_playlists"];
const EDITOR_OPS = OWNER_OPS.filter((o) => o !== "create_voice_note" && o !== "create_generation_human");

describe("Track page shells (U1-W2)", () => {
  test("Owner shape is pinned: comments, voice, records, crates, playlist and Agent context all present", () => {
    for (const h of [page(trackKnown), page(trackKnown, withAllowed(OWNER_OPS))]) {
      expect(h).toMatch(/<h2[^>]*>Comments<\/h2>/);
      expect(h).toContain('data-testid="track-curation"');
      expect(h).toContain('data-testid="add-to-crate"');
      expect(h).toMatch(/<h2[^>]*>Agent context<\/h2>/);
      expect(h).toContain('data-testid="copy-critique-prompt"');
      expect(h).toContain('data-testid="song-pr-panel"');
    }
  });
  test("a Listener sees no comment, voice, playlist, Records, Crates, tag or Agent context panel", () => {
    const h = page(trackKnown, withAllowed(LISTENER_OPS));
    expect(h).not.toMatch(/<h2[^>]*>Comments<\/h2>/);
    expect(h).not.toMatch(/<h2[^>]*>Agent context<\/h2>/);
    expect(h).not.toContain('data-testid="copy-critique-prompt"');
    expect(h).not.toContain('data-testid="song-pr-panel"');
    expect(h).not.toContain('data-testid="track-curation"');
    expect(h).not.toContain('data-testid="add-to-crate"');
    expect(h).not.toContain('data-testid="add-to-playlist"');
    expect(h).toContain('data-testid="track-play"');
  });
  test("an Editor keeps comments and curation but not the voice recorder", () => {
    const owner = page(trackKnown, withAllowed(OWNER_OPS));
    const editor = page(trackKnown, withAllowed(EDITOR_OPS));
    expect(editor).toMatch(/<h2[^>]*>Comments<\/h2>/);
    expect(editor).toContain('data-testid="track-curation"');
    expect(owner.length).toBeGreaterThan(editor.length);
    expect(owner).toMatch(/voice/i);
    expect(editor).not.toMatch(/voice/i);
  });
});

describe("Track page ratings (U1-W2)", () => {
  const withRating = (rating: unknown) => ({ ...trackKnown, rating }) as unknown as typeof trackKnown;
  const own = { by: { id: "u1", kind: "human", name: "x" }, stars: 4, updated_at: "x", verdict: null };
  test("Owner: other raters and the average are listed", () => {
    const h = page(withRating({ track_id: "t", critic: null, human: own, mine: own, others: [{ user: "bob", stars: 2, verdict: "meh", updated_at: "x" }], average: { mean: 3, n: 2 } }));
    expect(h).toContain('data-testid="rating-others"');
    expect(h).toContain("bob");
    expect(h).toContain("average 3 from 2 ratings");
  });
  test("Editor: the average, no others", () => {
    const h = page(withRating({ track_id: "t", critic: null, human: own, mine: own, average: { mean: 4, n: 1 } }));
    expect(h).toContain('data-testid="rating-average"');
    expect(h).not.toContain('data-testid="rating-others"');
  });
  test("Listener: nothing but their own stars; no panel at all", () => {
    const h = page(withRating({ track_id: "t", critic: null, human: own, mine: own }));
    expect(h).not.toContain('data-testid="rating-panel"');
    expect(h).not.toContain("average");
  });
});

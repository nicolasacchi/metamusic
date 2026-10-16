import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Track from "./Track.svelte";
import { trackKnown, trackLossy, trackReference } from "../lib/fixtures/m2c";

const page = (t: typeof trackKnown) => render(Track, { props: { id: t.id, initial: t } } as never).body;

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

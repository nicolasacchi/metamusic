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

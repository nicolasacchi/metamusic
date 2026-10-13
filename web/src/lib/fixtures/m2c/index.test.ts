import { describe, expect, test } from "bun:test";
import { duelOpenBlindLossy, duelRevealedLossy, mediaLinksLossy, summaryKnown, summaryLossy, summaryReference, trackKnown, trackLossy, trackReference } from "./index";

describe("M2c fixtures", () => {
  test("the three track kinds differ in recipe_known and lossy", () => {
    expect([summaryKnown.recipe_known, summaryKnown.lossy]).toEqual([true, false]);
    expect([summaryReference.recipe_known, summaryReference.lossy]).toEqual([false, false]);
    expect([summaryLossy.recipe_known, summaryLossy.lossy]).toEqual([false, true]);
    expect(trackKnown.recipe.engine).toBe("yue2");
    expect(trackReference.recipe.engine).toBe("unknown");
    expect(trackLossy.audio.mp3).toBeNull();
    expect(trackReference.audio.mp3).not.toBeNull();
  });

  test("lossy media links carry no FLAC and an mp3 master", () => {
    expect(mediaLinksLossy.flac).toBeNull();
    expect(mediaLinksLossy.lossy).toBe(true);
    expect(mediaLinksLossy.mp3.bitrate_kbps).toBe(320);
    expect(mediaLinksLossy.mp3.url.includes(".mp3")).toBe(true);
  });

  test("the blind duel hides tracks; the revealed one shows lossy tracks", () => {
    expect(duelOpenBlindLossy.slots.a.track).toBeNull();
    expect(duelOpenBlindLossy.slots.b.track).toBeNull();
    expect(duelRevealedLossy.slots.a.track?.lossy).toBe(true);
    expect(duelRevealedLossy.slots.b.track?.recipe_known).toBe(false);
  });
});

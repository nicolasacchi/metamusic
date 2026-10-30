import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import PlayerBar from "./PlayerBar.svelte";
import { judge } from "../judge.svelte";

// A `tape:<id>` current item is not a take: the bar offers no take link, rating or comments for it.
const bar = (trackId: string) => {
  const player = { title: "T", album: "", hasNext: false, state: { trackId, playing: false, current: 0, duration: 0, restored: null, held: false, noFlac: false, error: "" } };
  return render(PlayerBar as never, { props: { player } } as never).body;
};

describe("PlayerBar with a tape item", () => {
  judge.seed("take1", { stars: 3, verdict: null, critic: null, status: "candidate", tags: [] } as never);
  test("a take shows its link, rating and comment button", () => {
    const h = bar("take1");
    expect(h).toContain('href="/tracks/take1"');
    expect(h).toContain('data-testid="player-judge"');
  });
  test("a tape shows none of them", () => {
    judge.seed("tape:abc", { stars: 3, verdict: null, critic: null, status: "candidate", tags: [] } as never);
    const h = bar("tape:abc");
    expect(h).not.toContain("/tracks/");
    expect(h).not.toContain("player-judge");
    expect(h).not.toContain("player-comment");
  });
});

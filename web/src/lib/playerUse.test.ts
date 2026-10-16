import { describe, expect, test } from "bun:test";
import { playerInUse } from "./playerUse";

const idle = { playing: false, pendingPlay: false, trackId: "", queue: [] as string[] };

describe("playerInUse", () => {
  test("an idle player is not in use", () => expect(playerInUse(idle)).toBe(false));
  test.each([
    ["playing", { playing: true }],
    ["tap to continue", { pendingPlay: true }],
    ["paused with a loaded track", { trackId: "t1" }],
    ["between tracks with a queue", { queue: ["t2"] }],
  ])("%s counts as in use", (_n, patch) => expect(playerInUse({ ...idle, ...patch })).toBe(true));
});

import { describe, expect, test } from "bun:test";
import { trackPageQueue } from "./queue";

describe("track page queue", () => {
  test("a track that is already queued keeps the queue", () => {
    expect(trackPageQueue("b", ["a", "b", "c"], [{ id: "z" }])).toBeUndefined();
  });
  test("otherwise the track is followed by its song group, once each", () => {
    expect(trackPageQueue("a", [], [{ id: "x" }, { id: "a" }, { id: "y" }, { id: "x" }])).toEqual(["a", "x", "y"]);
  });
  test("a track without siblings queues alone (ends with the visible end of queue)", () => {
    expect(trackPageQueue("a", ["q"], [])).toEqual(["a"]);
  });
});

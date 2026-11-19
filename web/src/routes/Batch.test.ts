// U1-INT-W: an Editor's own awaiting_confirmation batch shows "waiting", never the Approve link; the Owner sees the link.
import { afterEach, describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import BatchReview from "../lib/components/BatchReview.svelte";
import { me } from "../lib/me.svelte";

const as = (allowed: string[]) => {
  me.allowed = allowed;
  me.loaded = true;
};
afterEach(() => {
  me.allowed = [];
  me.loaded = false;
});

describe("batch review banner", () => {
  test("Owner sees the Approve link", () => {
    as(["approve_batch", "get_batch"]);
    const h = render(BatchReview, { props: { id: "b1" } } as never).body;
    expect(h).toContain('data-testid="review-approve"');
    expect(h).not.toContain("review-waiting");
  });
  test("Editor sees the waiting note and no link", () => {
    as(["get_batch", "create_batch"]);
    const h = render(BatchReview, { props: { id: "b1" } } as never).body;
    expect(h).toContain('data-testid="review-waiting"');
    expect(h).not.toContain("review-approve");
  });
  test("before me loads neither note shows (no flash)", () => {
    const h = render(BatchReview, { props: { id: "b1" } } as never).body;
    expect(h).not.toContain("review-approve");
    expect(h).not.toContain("review-waiting");
  });
});

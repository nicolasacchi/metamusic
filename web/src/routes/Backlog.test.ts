import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Backlog from "./Backlog.svelte";

describe("Backlog page", () => {
  test("renders exactly one Muse Board link", () => {
    const h = render(Backlog, { props: { search: "", navigate: () => {} } } as never).body;
    expect(h.split('data-testid="board-link"').length - 1).toBe(1);
  });
});

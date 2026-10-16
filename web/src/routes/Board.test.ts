import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Board from "./Board.svelte";
import { bigBoard, board } from "../lib/fixtures/l1";

const page = (b = board, search = "") => render(Board, { props: { search, navigate: () => {}, initial: b, viewer: { kind: "human", scopes: ["read", "generate"] } } } as never).body;

describe("Board page", () => {
  const h = page();
  test("renders an ARIA grid with the Muse rows, (none) column and Unassigned row last", () => {
    expect(h).toContain('role="grid"');
    expect(h).toContain("Synthetic muse A");
    expect(h).toContain("(none)");
    expect(h.indexOf("Synthetic muse B")).toBeLessThan(h.indexOf("Unassigned"));
    expect(h.indexOf("remake-long")).toBeLessThan(h.indexOf("(none)"));
  });
  test("chips are toggle buttons with aria-pressed and a state glyph", () => {
    expect((h.match(/data-testid="song-chip"/g) ?? []).length).toBe(7);
    expect(h).toMatch(/<button[^>]*aria-pressed="false"[^>]*data-testid="song-chip"/);
    expect(h).toContain("×4");
    expect(h).toContain('data-testid="chip-queued"');
    expect(h).toContain("tempo mismatch");
  });
  test("the selection bar shows the cap and Estimate starts disabled", () => {
    expect(h).toContain("0 selected");
    expect(h).toMatch(/<button[^>]*disabled[^>]*data-testid="estimate"/);
    expect(h).toContain("limit 50 drafts per batch");
  });
  test("links to the Muse page and offers the Sessions panel", () => {
    expect(h).toContain('href="/muses/muse-a"');
    expect(h).toContain('data-testid="session-panel"');
  });
  test("filters reflect the URL", () => {
    const f = page(board, "?state=draft&sound=era%3A90s&angle=acoustic");
    expect(f).toContain('data-testid="filter-state-draft"');
    expect(f).toMatch(/aria-pressed="true"[^>]*data-testid="filter-state-draft"/);
    expect(f).toContain("era:90s ×");
    expect(f).toContain("angle: acoustic ×");
  });
  test("an empty Board says so; a next_cursor offers Load more", () => {
    const e = page({ ...board, rows: [] });
    expect(e).toContain('data-testid="board-empty"');
    expect(page({ ...board, next_cursor: "c" })).toContain('data-testid="board-more"');
    expect(h).not.toContain('data-testid="board-more"');
  });
  test("a 600-chip Board renders", () => {
    expect((page(bigBoard(600)).match(/data-testid="song-chip"/g) ?? []).length).toBe(600);
  });
});

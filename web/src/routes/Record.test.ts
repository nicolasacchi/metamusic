import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Record from "./Record.svelte";
import Records from "./Records.svelte";
import HumanPublish from "./HumanPublish.svelte";
import { preview, recordView, slot } from "../lib/fixtures/l2/record";

const human = { kind: "human", scopes: ["read", "write"] };
const agent = { kind: "agent", scopes: ["read", "write"] };
const page = (v = recordView(3), viewer: unknown = human) => render(Record, { props: { id: "rec-1", initial: v, viewer } } as never).body;

describe("Record page", () => {
  test("studio, slots with up/down, tabs", () => {
    const h = page();
    expect(h).toContain('data-testid="record-studio"');
    expect((h.match(/data-testid="slot-row"/g) ?? []).length).toBe(3);
    expect((h.match(/data-testid="slot-up"/g) ?? []).length).toBe(3);
    expect((h.match(/data-testid="slot-down"/g) ?? []).length).toBe(3);
    for (const t of ["slots", "changelog", "liner", "energy", "tapes"]) expect(h).toContain(`data-testid="record-tab-${t}"`);
    expect(h).toContain('href="/human/records/rec-1/publish"');
  });
  test("warnings, undecided and pin-differs show", () => {
    const v = recordView(2, { warnings: [{ slot_id: "sl-1", code: "undecided_pick" }], totals: { slots: 2, playable: 2, duration_s: 10, lossy: 0, warnings: 1, undecided: 1 } });
    v.slots[0] = slot(1, { pin_differs: true, mode: "pinned", warnings: ["undecided_pick"], why: "newest take · undecided" });
    const h = page(v);
    expect(h).toContain("undecided");
    expect(h).toContain('data-testid="slot-pin-differs"');
    expect(h).toContain('data-testid="slot-warning"');
    expect(h).toContain('data-testid="slot-unpin"');
  });
  test("an agent cannot edit a published record and gets no publish link", () => {
    const v = recordView(2);
    v.record.status = "published";
    const h = page(v, agent);
    expect(h).not.toContain('data-testid="slot-up"');
    expect(h).not.toContain("/human/records/");
    expect(page(v, human)).toContain('data-testid="slot-up"');
  });
  test("at the edges the outer buttons are disabled", () => {
    const h = page(recordView(2));
    const first = h.slice(h.indexOf('data-slot-id="sl-1"'), h.indexOf('data-slot-id="sl-2"'));
    expect(first).toMatch(/disabled[^>]*data-testid="slot-up"|data-testid="slot-up"[^>]*disabled/);
  });
  test("no second audio element", () => {
    expect(page()).not.toContain("<audio");
  });
});

describe("Records list", () => {
  test("rows link to the record; New form present", () => {
    const item = { cover: recordView().cover, duration_s: 600, hidden: false, id: "rec-1", playable: 3, revision: 1, slots: 3, status: "draft", subtitle: "", title: "Synthetic record", updated_at: "x", warnings: 2 };
    const h = render(Records, { props: { initial: { items: [item], next_cursor: null, total: 1 } } } as never).body;
    expect(h).toContain('data-testid="record-row"');
    expect(h).toContain('href="/records/rec-1"');
    expect(h).toContain('data-testid="record-new"');
    expect(h).toContain("2 warnings");
  });
});

describe("HumanPublish", () => {
  const v = recordView(2, { preview });
  const h = render(HumanPublish, { props: { id: "rec-1", initial: v, navigate: () => {} } } as never).body;
  test("preview lists chapter names, tags, liner and warnings", () => {
    expect(h).toContain("Synthetic take 2 (lossy source)");
    expect(h).toContain("TITLE");
    expect(h).toContain("1. Synthetic song 1");
    expect(h).toContain("Slot 2");
    expect(h).toContain("undecided");
  });
  test("freeze shortcut and review checkbox are offered; publish waits for review", () => {
    expect(h).toContain('data-testid="publish-freeze"');
    expect(h).toContain('data-testid="publish-reviewed"');
    expect(h).toMatch(/disabled[^>]*data-testid="publish-go"|data-testid="publish-go"[^>]*disabled/);
  });
});

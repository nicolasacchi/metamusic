import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import AddToCrate from "../lib/components/AddToCrate.svelte";
import AddToRecord from "../lib/components/AddToRecord.svelte";
import CrateMembers from "../lib/components/CrateMembers.svelte";
import FilterBuilder from "../lib/components/FilterBuilder.svelte";
import Crate from "./Crate.svelte";
import Crates from "./Crates.svelte";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;
const crate = (over: Record<string, unknown> = {}) => ({
  id: "c1",
  name: "Synthetic crate",
  description: "",
  kind: "manual",
  subject: "song",
  filter: null,
  hidden: false,
  revision: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  created_by: { kind: "human", id: "u", name: "operator" },
  ...over,
});
const human = { kind: "human", scopes: ["read", "write"] };

describe("Crates list", () => {
  test("rows show kind, subject, live count and a smart Crate's filter", () => {
    const h = html(Crates, {
      initial: [
        { ...crate(), count: 3 },
        { ...crate({ id: "c2", name: "Smart one", kind: "smart", filter: { unheard: true, min_duration_s: 240 } }), count: 2 },
      ],
    });
    expect(h).toContain('data-testid="crates-list"');
    expect((h.match(/data-testid="crate-row"/g) ?? []).length).toBe(2);
    expect(h).toContain('href="/crates/c2"');
    expect(h).toContain("3 Songs");
    expect(h).toContain("Unheard");
    expect(h).toContain('data-testid="crate-new"');
  });
  test("an empty list explains what a Crate is", () => {
    expect(html(Crates, { initial: [] })).toContain('data-testid="crates-empty"');
  });
});

describe("Crate page", () => {
  test("manual: members with per-row remove, counts, no filter builder", () => {
    const h = html(Crate, { id: "c1", viewer: human, initial: { crate: crate(), count: 3, warnings: [] } });
    expect(h).toContain('data-testid="crate-page"');
    expect(h).toContain("Synthetic crate");
    expect(h).toContain('data-testid="crate-count"');
    expect(h).not.toContain('data-testid="crate-filter-builder"');
  });
  test("smart: filter builder, live count, save, and stale-id warnings", () => {
    const h = html(Crate, {
      id: "c2",
      viewer: human,
      initial: { crate: crate({ id: "c2", kind: "smart", filter: { sound: ["genre_family:reggae"], unheard: true } }), count: 2, warnings: ["muse_id m9 no longer exists"] },
    });
    expect(h).toContain('data-testid="crate-filter-builder"');
    expect(h).toContain('data-testid="crate-live-count"');
    expect(h).toContain('data-testid="crate-save-filter"');
    expect(h).toContain('data-testid="crate-warnings"');
    expect(h).toContain("muse_id m9 no longer exists");
  });
});

describe("CrateMembers", () => {
  const initial = { items: [{ id: "s1", title: "Synthetic song", detail: "rendered", songId: "s1", durationS: null }], next: "n", total: 5 };
  test("rows link to the Song; Remove only on a manual Crate; more when paged", () => {
    const m = html(CrateMembers, { subject: "song", query: { crate_id: "c1" }, removeFrom: "c1", initial });
    expect(m).toContain('href="/songs/s1"');
    expect(m).toContain('data-testid="member-remove"');
    expect(m).toContain('data-testid="members-more"');
    expect(m).toContain("1 of 5");
    expect(html(CrateMembers, { subject: "song", query: {}, initial })).not.toContain('data-testid="member-remove"');
  });
  test("Takes link to the Take page and show the duration", () => {
    const m = html(CrateMembers, { subject: "take", query: {}, initial: { items: [{ id: "t1", title: "Synthetic take", detail: "draft", songId: "s1", durationS: 125 }], next: null, total: 1 } });
    expect(m).toContain('href="/tracks/t1"');
    expect(m).toContain("2:05");
  });
});

describe("FilterBuilder", () => {
  test("one control per vocabulary key, with the new predicates", () => {
    const h = html(FilterBuilder, { subject: "song", value: { sound: ["genre_family:reggae"], unheard: true, min_duration_s: 240, pick_decided: false }, onchange: () => {}, count: 2 });
    for (const k of ["unheard", "pick_decided", "in_record", "min_duration_s", "sound", "state"]) expect(h).toContain(`data-key="${k}"`);
    expect(h).toContain("2</strong> Songs match");
    expect(html(FilterBuilder, { subject: "take", value: {}, onchange: () => {}, count: 1 })).not.toContain('data-key="pick_decided"');
  });
  test("min_stars with unrated=true shows the server's message and withholds the count", () => {
    const h = html(FilterBuilder, { subject: "song", value: { min_stars: 3, unrated: true }, onchange: () => {} });
    expect(h).toContain("cannot be combined with min_stars");
    expect(h).toContain("Fix the highlighted fields");
  });
  test("a key the vocabulary does not know is reported as dropped", () => {
    expect(html(FilterBuilder, { subject: "song", value: { bogus: 1 }, onchange: () => {}, count: 0 })).toContain("bogus");
  });
});

describe("AddToCrate / AddToRecord", () => {
  test("Crate menu lists manual visible Crates and the memberships", () => {
    const h = html(AddToCrate, { subject: "song", ids: ["s1"], member: [{ id: "c1", name: "Synthetic crate" }], initialCrates: [{ ...crate(), count: 1 }, { ...crate({ id: "c3", name: "Hidden", hidden: true }), count: 0 }, { ...crate({ id: "c4", name: "Smart", kind: "smart" }), count: 0 }] });
    expect(h).toContain('data-testid="crate-select"');
    expect(h).toContain("Synthetic crate");
    expect(h).not.toContain(">Hidden<");
    expect(h).not.toContain(">Smart<");
    expect(h).toContain('href="/crates/c1"');
  });
  const rec = (id: string, status: string, hidden = false) => ({ id, title: `Rec ${id}`, subtitle: "", status, hidden, revision: 1, slots: 0, playable: 0, duration_s: 0, updated_at: "", warnings: 0, cover: {} });
  test("a published Record is offered per recordView.canEdit (human and device, not agent)", () => {
    const records = [rec("r1", "draft"), rec("r2", "published")];
    const agent = html(AddToRecord, { songIds: ["s1"], initialRecords: records, viewer: { kind: "agent", scopes: ["read", "write"] } });
    expect(agent).toContain("Rec r1");
    expect(agent).not.toContain("Rec r2");
    expect(html(AddToRecord, { songIds: ["s1"], initialRecords: records, viewer: { kind: "human", scopes: ["read", "write"] } })).toContain("Rec r2");
  });
  test("more than 60 Songs: the cap note", () => {
    const h = html(AddToRecord, { songIds: Array.from({ length: 75 }, (_, i) => `s${i}`), initialRecords: [] });
    expect(h).toContain('data-testid="record-cap-note"');
    expect(h).toContain("15 left out");
  });
  test("Track page: offers to pin the Take", () => {
    expect(html(AddToRecord, { songIds: ["s1"], pinTake: "t1", initialRecords: [rec("r1", "draft")] })).toContain('data-testid="record-pin-take"');
  });
});

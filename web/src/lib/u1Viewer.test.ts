// U1 C313.2: view-model helpers decide from whoami.allowed_ops when the viewer carries it.
import { describe, expect, test } from "bun:test";
import { canGenerate, mayGenerate, type Viewer } from "./genIntent";
import { canEdit, canPublish } from "./recordView";
import { canDecide } from "./proposalView";
import { me } from "./me.svelte";
import { pollsApprovals, showsApprovalBadge } from "./approvals.svelte";

const v = (kind: Viewer["kind"], allowed: string[], scopes: string[] = []): Viewer => ({ kind, scopes, allowed });
const owner = v("human", ["create_generation_human", "create_generation", "update_record", "edit_record_slots", "publish_record", "decide_taxonomy_proposal"], ["read", "write", "generate"]);
const editor = v("human", ["create_generation", "edit_record_slots"], ["read", "write", "generate"]);
const listener = v("human", ["search_tracks", "rate_track"], ["listen", "rate"]);
const ownerDevice = v("device", ["update_record", "edit_record_slots"], ["read", "write"]);
const track = { recipe_known: true, lossy: false };

describe("generation entry points", () => {
  test("Owner and Editor may start one; Listener and the Owner's device may not", () => {
    expect(mayGenerate(owner)).toBe(true);
    expect(mayGenerate(editor)).toBe(true);
    expect(mayGenerate(listener)).toBe(false);
    expect(mayGenerate(ownerDevice)).toBe(false);
    expect(canGenerate(editor, track)).toBe(true);
    expect(canGenerate(editor, { recipe_known: false, lossy: false })).toBe(false);
    expect(canGenerate(listener, track)).toBe(false);
    expect(canGenerate(null, track)).toBe(false);
  });
  test("a viewer without allowed_ops keeps the kind and scopes rule", () => {
    expect(mayGenerate({ kind: "human", scopes: ["generate"] })).toBe(true);
    expect(mayGenerate({ kind: "device", scopes: ["generate"] })).toBe(false);
    expect(mayGenerate({ kind: "human", scopes: ["read"] })).toBe(false);
  });
});

describe("records and proposals", () => {
  test("a published Record needs update_record, a draft needs slot editing, publishing needs publish_record", () => {
    expect(canEdit(owner, { status: "published" })).toBe(true);
    expect(canEdit(editor, { status: "published" })).toBe(false);
    expect(canEdit(editor, { status: "draft" })).toBe(true);
    expect(canEdit(listener, { status: "draft" })).toBe(false);
    expect(canEdit(ownerDevice, { status: "published" })).toBe(true);
    expect(canPublish(owner)).toBe(true);
    expect(canPublish(editor)).toBe(false);
    expect(canPublish(ownerDevice)).toBe(false);
  });
  test("deciding taxonomy proposals follows the op", () => {
    expect(canDecide(owner)).toBe(true);
    expect(canDecide(editor)).toBe(false);
    expect(canDecide({ kind: "human", scopes: ["write"] })).toBe(true);
  });
});

describe("approvals polling (C313.3)", () => {
  function as(kind: "human" | "device", role: "owner" | "editor" | "listener" | null, allowed: string[]) {
    me.loaded = true;
    me.kind = kind;
    me.role = role;
    me.allowed = allowed;
  }
  test("the Owner polls and sees the badge; Editor and Listener do neither", () => {
    as("human", "owner", ["list_batches", "approve_batch"]);
    expect(pollsApprovals()).toBe(true);
    expect(showsApprovalBadge()).toBe(true);
    as("human", "editor", ["list_batches"]);
    expect(pollsApprovals()).toBe(false);
    expect(showsApprovalBadge()).toBe(false);
    as("human", "listener", []);
    expect(pollsApprovals()).toBe(false);
    expect(showsApprovalBadge()).toBe(false);
  });
  test("the Owner's device keeps the read-only list without a badge", () => {
    as("device", null, ["list_batches"]);
    expect(pollsApprovals()).toBe(true);
    expect(showsApprovalBadge()).toBe(false);
  });
});

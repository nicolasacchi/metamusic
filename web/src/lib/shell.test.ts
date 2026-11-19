// U1-W2: the Owner, Editor and Listener shells from the real role matrix (internal/api/testdata/role_matrix.golden),
// so a can() set here is the set the server computes, not a hand-written guess.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { APP_ROUTES } from "./router";
import { NAV_LINKS, routeAllowed, linkVisible } from "./nav";
import { canOp } from "./me";
import { mayOp, showsPanel, TRACK_PANEL_OPS, type TrackPanel } from "./shell";
import type { Viewer } from "./genIntent";
import { actionError, hasOwnerColumn, ownerLabel } from "./deviceView";

const rows = readFileSync(resolve(import.meta.dirname, "../../../internal/api/testdata/role_matrix.golden"), "utf8")
  .split("\n")
  .filter((l) => l && !l.startsWith("#"))
  .map((l) => l.split("\t"));
const col = { owner: 3, editor: 4, listener: 5 } as const;
const allowedFor = (r: keyof typeof col) => rows.filter((x) => x[col[r]] === "Y").map((x) => x[0]!);
const viewerFor = (r: keyof typeof col): Viewer => ({ kind: "human", scopes: [], allowed: allowedFor(r) });
const canFor = (r: keyof typeof col) => {
  const a = allowedFor(r);
  return (op: string) => canOp(a, op);
};
const PANELS = Object.keys(TRACK_PANEL_OPS) as TrackPanel[];

describe("can() sets from the role matrix", () => {
  test("sanity: the matrix parsed and the three roles differ", () => {
    expect(rows.length).toBeGreaterThan(100);
    expect(allowedFor("owner").length).toBeGreaterThan(allowedFor("editor").length);
    expect(allowedFor("editor").length).toBeGreaterThan(allowedFor("listener").length);
  });
  test("every panel op exists in the matrix", () => {
    const ids = new Set(rows.map((r) => r[0]));
    for (const op of Object.values(TRACK_PANEL_OPS)) expect(ids.has(op), op).toBe(true);
  });
  test("Owner keeps every Track panel (the Owner UI is unchanged)", () => {
    for (const p of PANELS) expect(showsPanel(viewerFor("owner"), p), p).toBe(true);
  });
  test("Editor keeps every editing panel except voice notes (Owner-only)", () => {
    for (const p of PANELS) expect(showsPanel(viewerFor("editor"), p), p).toBe(p !== "voice");
    expect(mayOp(viewerFor("editor"), "approve_batch")).toBe(false);
  });
  test("Listener loses comments, voice, playlist, Agent context, tags, crates, records and transforms", () => {
    for (const p of PANELS) expect(showsPanel(viewerFor("listener"), p), p).toBe(false);
    expect(mayOp(viewerFor("listener"), "rate_track")).toBe(true);
  });
  test("a viewer with no allowed list, or no viewer, keeps every panel", () => {
    for (const p of PANELS) {
      expect(showsPanel(null, p)).toBe(true);
      expect(showsPanel({ kind: "human", scopes: [] }, p)).toBe(true);
    }
  });
});

describe("devices and Subsonic routes are reachable for every human role (C313.4)", () => {
  test("Owner, Editor and Listener may open /human/devices and /human/subsonic and see their nav links", () => {
    for (const r of ["owner", "editor", "listener"] as const) {
      expect(routeAllowed("human-devices", canFor(r)), r).toBe(true);
      expect(routeAllowed("human-subsonic", canFor(r)), r).toBe(true);
      const shown = NAV_LINKS.filter((l) => linkVisible(l, canFor(r))).map((l) => l.label);
      expect(shown, r).toContain("Devices");
      expect(shown, r).toContain("Subsonic");
    }
  });
  test("the Owner's secure links are all still there; a Listener has no Tokens, Connectors, Spend or Approvals", () => {
    const owner = NAV_LINKS.filter((l) => l.group === "Secure" && linkVisible(l, canFor("owner"))).map((l) => l.label);
    expect(owner).toEqual(["Approvals", "Spend", "Tokens", "Devices", "Connectors", "Subsonic", "Opt-ins"]);
    const listener = NAV_LINKS.filter((l) => l.group === "Secure" && linkVisible(l, canFor("listener"))).map((l) => l.label);
    expect(listener).toEqual(["Devices", "Subsonic"]);
  });
  test("the Owner opens every route; the pages' side links follow can()", () => {
    for (const r of APP_ROUTES) expect(routeAllowed(r.name, canFor("owner")), r.name).toBe(true);
    expect(canFor("owner")("list_tokens")).toBe(true);
    expect(canFor("listener")("list_tokens")).toBe(false);
    expect(canFor("listener")("list_connector_grants")).toBe(false);
  });
  test("the self-service ops are open to all three roles", () => {
    for (const r of ["owner", "editor", "listener"] as const)
      for (const op of ["list_devices", "create_pairing_code", "revoke_device", "revoke_own_device", "list_subsonic_clients", "create_subsonic_client", "revoke_subsonic_client"])
        expect(canFor(r)(op), `${r} ${op}`).toBe(true);
  });
});

describe("device rows", () => {
  test("the owner column shows only when a row carries owner", () => {
    expect(hasOwnerColumn([{ owner: "owner" }, { owner: "bob" }])).toBe(true);
    expect(hasOwnerColumn([{}, {}])).toBe(false);
    expect(hasOwnerColumn([])).toBe(false);
    expect(hasOwnerColumn(null)).toBe(false);
    expect(ownerLabel("owner")).toBe("Owner");
    expect(ownerLabel("bob")).toBe("bob");
    expect(ownerLabel(undefined)).toBe("");
  });
  test("DEVICE_LIMIT gets a clear message, other errors keep theirs, unknown shapes fall back", () => {
    expect(actionError({ code: "DEVICE_LIMIT", message: "raw" }, "x")).toContain("limit");
    expect(actionError({ code: "DEVICE_LIMIT", message: "raw" }, "x")).not.toContain("raw");
    expect(actionError({ code: "BAD", message: "nope" }, "x")).toBe("nope");
    expect(actionError(undefined, "fallback")).toBe("fallback");
    expect(actionError({ message: "" }, "fallback")).toBe("fallback");
  });
});

import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import Transforms from "./Transforms.svelte";
import TransformPanel from "../lib/components/TransformPanel.svelte";
import { caps, view } from "../lib/fixtures/t1/transforms";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;

describe("Transforms page", () => {
  const items = [
    view("done", { id: "d1", take_id: "d1", title: "Finished one", created_at: "2026-11-03T09:00:00Z" }),
    view("running", { id: "r1", title: "Live one", created_at: "2026-11-03T08:00:00Z" }),
    view("failed", { id: "f1", title: "Broken one", error: { code: "LOUDNESS_UNSAFE", message: "x" }, created_at: "2026-11-03T07:00:00Z" }),
  ];
  test("lists live rows first, Cancel only on the live one, link to the Take on the done one", () => {
    const h = html(Transforms, { initial: { caps, items } });
    expect(h.indexOf("Live one")).toBeLessThan(h.indexOf("Finished one"));
    expect((h.match(/data-testid="transform-cancel"/g) ?? []).length).toBe(1);
    expect(h).toContain('href="/tracks/d1"');
    expect(h).toContain('role="progressbar"');
    expect(h).toContain("loudness");
  });
  test("shows the shared-pool meter", () => {
    const h = html(Transforms, { initial: { caps, items } });
    expect(h).toContain('data-testid="transforms-quota"');
    expect(h).toContain("shared by every human");
    expect(h).toContain("2 min of 10 min used");
  });
  test("an empty history says how to start", () => {
    expect(html(Transforms, { initial: { caps, items: [] } })).toContain('data-testid="transforms-empty"');
  });
  test("501 is a not-available state, not an error", () => {
    const h = html(Transforms, { initial: { refusal: { status: 501, code: "NOT_IMPLEMENTED", message: "", details: {} } } });
    expect(h).toContain('data-testid="transforms-unavailable"');
    expect(h).not.toContain('role="alert"');
    expect(h).not.toContain('data-testid="transforms-list"');
  });
  test("loading before the first answer", () => {
    expect(html(Transforms, {})).toContain('data-testid="transforms-loading"');
  });
});

describe("Transform panel", () => {
  test("renders one button per listed kind in panel order, including the hi-fi kinds, and the first kind's form", () => {
    const h = html(TransformPanel, { trackId: "t1", initialCaps: caps });
    for (const k of ["morph", "shepard", "glacier", "polytempo", "ensemble", "master", "widen", "binaural"]) expect(h).toContain(`data-testid="transform-kind-${k}"`);
    expect(h).not.toContain('data-testid="transform-kind-score_render"');
    expect(h.indexOf("transform-kind-master")).toBeLessThan(h.indexOf("transform-kind-binaural"));
    expect(h).toContain('data-testid="transform-render"');
    expect(h).toContain('aria-pressed="true" data-testid="transform-kind-morph"');
    expect(h).toContain('data-testid="tf-partner"');
    expect(h).toContain('data-testid="tf-downbeat_a_s-auto"');
  });
  test("the panel never widens the page: fields wrap and the grid is bounded", () => {
    const h = html(TransformPanel, { trackId: "t1", initialCaps: caps });
    expect(h).toContain('role="group" aria-label="Kind of transform"');
    expect(h).not.toMatch(/<table/);
  });
  test("a disabled server shows not available", () => {
    const h = html(TransformPanel, { trackId: "t1", initialCaps: { ...caps, enabled: false } });
    expect(h).toContain('data-testid="transform-unavailable"');
    expect(h).not.toContain('data-testid="transform-render"');
  });
  test("loading state before capabilities arrive", () => {
    expect(html(TransformPanel, { trackId: "t1" })).toContain('data-testid="transform-loading"');
  });
});

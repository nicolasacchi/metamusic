import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import {
  acceptAllBody,
  acceptSubsetBody,
  canDecide,
  decideErrorText,
  impactText,
  mappingText,
  rejectBody,
  resultText,
  selectionBody,
  STALE_TEXT,
} from "./proposalView";

type Impact = components["schemas"]["TaxonomyImpact"];
type Result = components["schemas"]["DecideResult"];

const impact = (over: Partial<Impact> = {}): Impact => ({ gain: [], loss: [], songs_scanned: 20, taxonomy_version: 3, truncated: false, unmapped_cleared: [], ...over });

describe("impactText", () => {
  test("gain, loss and cleared terms in one sentence", () => {
    const t = impactText(
      impact({
        gain: [{ facet: "genre_family", value: "reggae", songs: 14 }],
        loss: [{ facet: "genre_family", value: "pop", songs: 2 }],
        unmapped_cleared: [{ term: "skank", songs: 14 }],
      }),
    );
    expect(t).toBe("14 Songs gain reggae, 2 lose pop; clears 'skank' in 14 Songs");
  });
  test("singular, several values, cleared list", () => {
    expect(impactText(impact({ gain: [{ facet: "mood", value: "dark", songs: 1 }, { facet: "mood", value: "calm", songs: 3 }] }))).toBe("1 Song gains dark, 3 gain calm");
    expect(impactText(impact({ unmapped_cleared: [{ term: "a", songs: 1 }, { term: "b", songs: 5 }] }))).toBe("clears 'a' in 1 Song, 'b' in 5 Songs");
    expect(impactText(impact({ loss: [{ facet: "era", value: "80s", songs: 1 }] }))).toBe("1 Song loses 80s");
  });
  test("nothing changes; truncated scan is stated", () => {
    expect(impactText(impact())).toBe("No Songs change");
    expect(impactText(impact({ truncated: true, songs_scanned: 500 }))).toBe("No Songs change (first 500 Songs scanned)");
  });
});

describe("mappingText", () => {
  test("op, term, facet, value", () => {
    expect(mappingText({ op: "add", term: "skank", facet: "genre_family", value: "reggae" })).toBe("add 'skank' → Genre: reggae");
    expect(mappingText({ op: "remove", term: "x", facet: "mood", value: "dark" })).toBe("remove 'x' → Mood: dark");
  });
});

describe("decision bodies", () => {
  test("accept all omits accept_mappings", () => {
    expect(acceptAllBody("ok")).toEqual({ decision: "accept", note: "ok" });
    expect(acceptAllBody()).toEqual({ decision: "accept" });
  });
  test("a subset becomes sorted distinct indices", () => {
    expect(acceptSubsetBody(new Set([2, 0, 2]))).toEqual({ decision: "accept", accept_mappings: [0, 2] });
  });
  test("selectionBody: everything selected is accept all; a subset is by index; none is null", () => {
    expect(selectionBody(new Set([0, 1, 2]), 3)).toEqual({ decision: "accept" });
    expect(selectionBody(new Set([1]), 3)).toEqual({ decision: "accept", accept_mappings: [1] });
    expect(selectionBody(new Set(), 3)).toBeNull();
  });
  test("reject carries a trimmed note only when given", () => {
    expect(rejectBody("  no  ")).toEqual({ decision: "reject", note: "no" });
    expect(rejectBody("  ")).toEqual({ decision: "reject" });
  });
});

describe("result and error text", () => {
  const prop = { state: "accepted" } as Result["proposal"];
  test("accepted: version, recomputed counts and stale skips", () => {
    const r: Result = {
      proposal: prop,
      applied: { mappings_applied: 2, unchanged: 0, version: 7, recomputed: { songs: 14, takes: 30 } },
      skipped: [{ index: 1, reason: "stale" }],
    };
    expect(resultText(r)).toBe("Applied 2 mappings as taxonomy version 7; recomputed 14 songs and 30 takes. Skipped 1 as stale.");
  });
  test("singular and no skips", () => {
    const r: Result = { proposal: prop, applied: { mappings_applied: 1, unchanged: 0, version: 4, recomputed: { songs: 1, takes: 1 } }, skipped: [] };
    expect(resultText(r)).toBe("Applied 1 mapping as taxonomy version 4; recomputed 1 songs and 1 takes.");
  });
  test("rejected", () => {
    expect(resultText({ proposal: { state: "rejected" } as Result["proposal"], skipped: [] })).toBe("Rejected.");
  });
  test("stale and closed errors", () => {
    expect(decideErrorText("TAXONOMY_STALE", "x")).toBe(STALE_TEXT);
    expect(STALE_TEXT).toBe("Nothing left to apply — proposal expired");
    expect(decideErrorText("TAXONOMY_PROPOSAL_CLOSED", "x")).toBe("This proposal was already decided.");
    expect(decideErrorText("HUMAN_ONLY", "x")).toBe("Only the operator can decide a proposal.");
    expect(decideErrorText("OTHER", "boom")).toBe("boom");
    expect(decideErrorText(undefined, "")).toBe("The decision failed");
  });
});

describe("canDecide", () => {
  test("only a human with write scope; agents, devices and unknown are read-only", () => {
    expect(canDecide({ kind: "human", scopes: ["read", "write"] })).toBe(true);
    expect(canDecide({ kind: "human", scopes: ["read"] })).toBe(false);
    expect(canDecide({ kind: "agent", scopes: ["read", "write"] })).toBe(false);
    expect(canDecide({ kind: "device", scopes: ["read", "write"] })).toBe(false);
    expect(canDecide(null)).toBe(false);
  });
});

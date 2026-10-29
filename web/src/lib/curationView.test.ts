import { expect, test } from "bun:test";
import { TOGGLES, patchFor, valueOf } from "./curationView";

test("two opt-ins, each explained in both positions", () => {
  expect(TOGGLES.map((t) => t.key)).toEqual(["export_muse_names", "subsonic_muse_names"]);
  for (const t of TOGGLES) {
    expect(t.off.startsWith("Off:")).toBe(true);
    expect(t.on.startsWith("On:")).toBe(true);
  }
  expect(TOGGLES[0]!.off).toContain("never contain an inspiration's name");
  expect(TOGGLES[1]!.note).toContain("automatic albums");
});

test("a change sends only its own key", () => {
  expect(patchFor("subsonic_muse_names", true)).toEqual({ subsonic_muse_names: true });
  expect(valueOf({ export_muse_names: false, subsonic_muse_names: true }, "subsonic_muse_names")).toBe(true);
});

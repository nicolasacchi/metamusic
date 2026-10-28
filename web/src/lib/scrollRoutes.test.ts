import { expect, test } from "bun:test";
import { READY_ROUTES } from "./scrollRestore";

test("L2 list and detail routes signal readiness so Back restores the scroll position", () => {
  for (const n of ["records", "record", "crates", "crate"]) expect(READY_ROUTES.has(n), n).toBe(true);
});

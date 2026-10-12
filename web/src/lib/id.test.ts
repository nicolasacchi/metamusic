import { expect, test } from "bun:test";
import { randomId } from "./id";

test("randomId works without crypto.randomUUID (insecure context)", () => {
  const orig = crypto.randomUUID;
  // @ts-expect-error simulate insecure context
  crypto.randomUUID = undefined;
  try {
    const a = randomId();
    expect(a).toMatch(/^[0-9a-f]{16}$/);
    expect(randomId()).not.toBe(a);
  } finally {
    crypto.randomUUID = orig;
  }
});

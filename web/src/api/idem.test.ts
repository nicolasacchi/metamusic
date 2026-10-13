import { expect, test } from "bun:test";
import { idempotentAction, newIdempotencyKey } from "./idem";

const V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

test("newIdempotencyKey is a uuidv7", () => {
  expect(newIdempotencyKey()).toMatch(V7);
  expect(newIdempotencyKey()).not.toBe(newIdempotencyKey());
});

test("one action keeps its key across retries; a new action gets a fresh one", async () => {
  const seen: string[] = [];
  const first = idempotentAction(async (key) => void seen.push(key));
  await first();
  await first();
  const second = idempotentAction(async (key) => void seen.push(key));
  await second();
  expect(seen[0]).toBe(seen[1]!);
  expect(seen[2]).not.toBe(seen[0]!);
  expect(seen[2]).toMatch(V7);
});

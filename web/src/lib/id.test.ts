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

import { uuidv7 } from "./id";

const V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

test("uuidv7 has version 7 and the RFC 9562 variant", () => {
  for (let i = 0; i < 50; i++) expect(uuidv7()).toMatch(V7);
});

test("uuidv7: 1000 ids in one mocked millisecond are strictly increasing", () => {
  const realNow = Date.now;
  Date.now = () => 1_760_000_000_000;
  try {
    const ids = Array.from({ length: 1000 }, () => uuidv7());
    for (let i = 1; i < ids.length; i++) expect(ids[i]! > ids[i - 1]!).toBe(true);
  } finally {
    Date.now = realNow;
  }
});

test("uuidv7 stays monotonic when the clock steps back", () => {
  const realNow = Date.now;
  try {
    Date.now = () => 1_760_000_100_000;
    const a = uuidv7();
    Date.now = () => 1_760_000_099_000;
    expect(uuidv7() > a).toBe(true);
  } finally {
    Date.now = realNow;
  }
});

test("uuidv7 works with crypto.randomUUID deleted", () => {
  const orig = crypto.randomUUID;
  // @ts-expect-error simulate insecure context
  crypto.randomUUID = undefined;
  try {
    expect(uuidv7()).toMatch(V7);
  } finally {
    crypto.randomUUID = orig;
  }
});

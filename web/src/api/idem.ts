// One idempotency key per user action, kept across retries of that action.
import { uuidv7 } from "../lib/id";

export function newIdempotencyKey(): string {
  return uuidv7();
}

/**
 * Wraps one user action: every call of the returned function (a retry) sends the same key;
 * a new action creates a new wrapper and so gets a fresh key.
 */
export function idempotentAction<T>(send: (key: string) => Promise<T>): () => Promise<T> {
  const key = newIdempotencyKey();
  return () => send(key);
}

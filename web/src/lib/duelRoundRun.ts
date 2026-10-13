// Runs a blind round: sequential create_duel calls, one idempotency key per pair that survives every
// retry of that pair (rate limit, network error, 5xx).
import { RateLimited } from "../api/client";
import { idempotentAction } from "../api/idem";
import type { Pair } from "./duelRound";

export type CreateDuel = (pair: Pair, idempotencyKey: string) => Promise<{ id: string }>;

export interface RoundProgress {
  done: number;
  total: number;
  /** Seconds left of a rate-limit wait; 0 when not waiting. */
  waitingS: number;
}

export interface RoundOptions {
  create: CreateDuel;
  navigate: (to: string) => void;
  sleep?: (ms: number) => Promise<void>;
  onProgress?: (p: RoundProgress) => void;
  /** Attempts per pair (default 6). */
  maxAttempts?: number;
}

export interface RoundResult {
  created: string[];
  error?: string;
}

/** An error carrying an HTTP status (thrown by the page's create adapter for non-2xx answers). */
export class HttpError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "HttpError";
    this.status = status;
  }
}

function retryable(e: unknown): boolean {
  return e instanceof RateLimited || e instanceof TypeError || (e instanceof HttpError && e.status >= 500);
}

const realSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export async function runBlindRound(pairs: readonly Pair[], o: RoundOptions): Promise<RoundResult> {
  const sleep = o.sleep ?? realSleep;
  const max = o.maxAttempts ?? 6;
  const created: string[] = [];
  const report = (waitingS: number) => o.onProgress?.({ done: created.length, total: pairs.length, waitingS });
  const finish = (error?: string): RoundResult => {
    if (created.length > 0) o.navigate(`/duels/${created[0]}`);
    return error === undefined ? { created } : { created, error };
  };
  for (const pair of pairs) {
    report(0);
    const send = idempotentAction((key) => o.create(pair, key));
    let last: unknown;
    let ok = false;
    for (let attempt = 1; attempt <= max && !ok; attempt++) {
      try {
        created.push((await send()).id);
        ok = true;
      } catch (e) {
        last = e;
        if (!retryable(e)) return finish(e instanceof Error ? e.message : "Could not create the duel");
        if (attempt === max) break;
        if (e instanceof RateLimited) {
          report(e.retryAfterS);
          await sleep(e.retryAfterS * 1000);
        } else {
          await sleep(Math.min(2000, 250 * attempt));
        }
      }
    }
    if (!ok) return finish(last instanceof Error ? last.message : "Could not create the duel");
  }
  report(0);
  return finish();
}

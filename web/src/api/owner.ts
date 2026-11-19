// Owner-side narrowing. U1-E1 made some money and creator fields optional on the wire because the
// server omits them for an Editor or Listener. The Owner's pages (Jobs, Human*, Receipts, batch and
// job views) are only ever served the full shape, so they narrow the generated optional fields here.
import type { components } from "./schema";

type S = components["schemas"];

export type Owned<T, K extends keyof T> = T & { [P in K]-?: NonNullable<T[P]> };

/** Type-only narrowing: no runtime check; the Owner's wire shape always carries these fields. */
export function owned<T, K extends keyof T>(x: T): Owned<T, K> {
  return x as Owned<T, K>;
}

export type OwnerBatchSummary = Owned<S["BatchSummary"], "cost" | "created_by">;
export type OwnerBatch = Owned<S["Batch"], "cost" | "created_by">;
export type OwnerJobSummary = Owned<S["JobSummary"], "cost">;
export type OwnerJob = Owned<S["Job"], "cost">;
export type OwnerBudget = Owned<S["Budget"], "caps" | "month" | "day" | "balance" | "auto_approve">;
export type OwnerEstimateBudget = Owned<S["EstimateBudget"], "month" | "day" | "batch_cap_micro_usd">;

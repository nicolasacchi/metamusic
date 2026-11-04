// Receipts (C262, D-M5-R3-5): a web-only grouping of get_audit pages by principal with a 30 minute gap, money from list_batches.
// Scaffold by M5-F2 (the constant only); lane M5-W2 adds the grouping and ReceiptsTable.

/** A new group starts after this much quiet between two audit rows of the same principal. */
export const RECEIPT_GAP_MS = 30 * 60 * 1000;

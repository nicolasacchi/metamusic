// Pure helpers for My devices and the Subsonic clients page (U1 C313.4, D1 Revision 14).

/** Does any row carry an owner? The server adds `owner` only to the Owner's listing, so field presence drives the column. */
export function hasOwnerColumn(items: readonly { owner?: unknown }[] | null | undefined): boolean {
  return !!items && items.some((i) => typeof i.owner === "string");
}

/** "owner" is the Owner's key; show the row as the Owner's. */
export function ownerLabel(o: string | undefined): string {
  return o === undefined ? "" : o === "owner" ? "Owner" : o;
}

const LIMIT_MESSAGE = "You have reached the limit of paired devices and clients. Unpair or revoke one you no longer use, then try again.";

/** The line shown for a failed create or revoke; a 422 DEVICE_LIMIT gets a clear message instead of the raw one. */
export function actionError(error: unknown, fallback: string): string {
  if (typeof error === "object" && error !== null) {
    const e = error as { code?: unknown; message?: unknown };
    if (e.code === "DEVICE_LIMIT") return LIMIT_MESSAGE;
    if (typeof e.message === "string" && e.message !== "") return e.message;
  }
  return fallback;
}

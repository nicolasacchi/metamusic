// Import run section items arrive as opaque objects (C19); render them as plain text lines.
export function describeItem(item: unknown): string {
  if (item === null || item === undefined) return "";
  if (typeof item !== "object") return String(item);
  const parts: string[] = [];
  for (const [k, v] of Object.entries(item as Record<string, unknown>)) {
    if (v === null || v === undefined || v === "") continue;
    parts.push(`${k}: ${typeof v === "object" ? JSON.stringify(v) : String(v)}`);
  }
  return parts.join(" · ");
}

/** Summary counters shown for a run: numeric entries of `summary`, in key order. */
export function summaryCounts(summary: Record<string, unknown> | null): [string, number][] {
  if (!summary) return [];
  return Object.entries(summary)
    .filter((e): e is [string, number] => typeof e[1] === "number")
    .sort(([a], [b]) => a.localeCompare(b));
}

export interface TimedSection {
  start_s: number | null;
  end_s: number | null;
}

/** Index of the section containing t, or -1 (also when sections are untimed). */
export function sectionAt(sections: readonly TimedSection[], t: number): number {
  for (let i = 0; i < sections.length; i++) {
    const s = sections[i]!;
    if (s.start_s == null) continue;
    let end = s.end_s;
    if (end == null) {
      const next = sections.slice(i + 1).find((x) => x.start_s != null);
      end = next ? next.start_s : Infinity;
    }
    if (t >= s.start_s && t < (end as number)) return i;
  }
  return -1;
}

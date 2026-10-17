// "Song · angle" for list rows and Judge cards. Null-safe: either part (or both) may be missing, and a take without a
// Song has no line at all.
export interface SongRef {
  song_title?: string | null;
  song_angle?: string | null;
}

export function songLine(t: SongRef): string {
  return [t.song_title, t.song_angle]
    .map((x) => (x ?? "").trim())
    .filter(Boolean)
    .join(" · ");
}

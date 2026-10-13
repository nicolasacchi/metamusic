// Click on the spectrogram image -> seconds (C54.4). The image may be rendered smaller than its
// natural size; `plot` is in natural pixels.
export interface SeekPlot {
  x0: number;
  width: number;
  duration_s: number;
}

export function spectroSeek(x: number, renderedWidth: number, naturalWidth: number, plot: SeekPlot): number {
  if (![x, renderedWidth, naturalWidth, plot.x0, plot.width, plot.duration_s].every(Number.isFinite)) return 0;
  if (renderedWidth <= 0 || naturalWidth <= 0 || plot.width <= 0) return 0;
  const natural = (x * naturalWidth) / renderedWidth;
  const f = Math.min(1, Math.max(0, (natural - plot.x0) / plot.width));
  return f * plot.duration_s;
}

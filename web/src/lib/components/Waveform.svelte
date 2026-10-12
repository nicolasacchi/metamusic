<script lang="ts">
  import { peaksToColumns } from "../peaks";

  interface Props {
    /** Interleaved [min0,max0,...] samples, or null while loading. */
    peaks: readonly number[] | null;
    duration: number;
    current: number;
    onseek: (seconds: number) => void;
    height?: number;
    label?: string;
  }
  let { peaks, duration, current, onseek, height = 48, label = "Waveform" }: Props = $props();

  let canvas: HTMLCanvasElement;
  let cssWidth = $state(0);

  function draw() {
    if (!canvas || cssWidth <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    const w = Math.max(1, Math.floor(cssWidth * dpr));
    const h = Math.max(1, Math.floor(height * dpr));
    if (canvas.width !== w) canvas.width = w;
    if (canvas.height !== h) canvas.height = h;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.clearRect(0, 0, w, h);
    const cols = peaks ? peaksToColumns(peaks, Math.floor(peaks.length / 2), w) : [];
    const playedX = duration > 0 ? Math.min(w, (current / duration) * w) : 0;
    const mid = h / 2;
    const scale = (h / 2) * 0.95;
    // Peak values are normalised int8/int16 in the source; scale by the observed extreme.
    let ext = 1;
    for (const c of cols) ext = Math.max(ext, Math.abs(c.min), Math.abs(c.max));
    cols.forEach((c, x) => {
      ctx.fillStyle = x < playedX ? "#f2a65a" : "#7a6a5c";
      const top = mid - (c.max / ext) * scale;
      const bot = mid - (c.min / ext) * scale;
      ctx.fillRect(x, top, 1, Math.max(1, bot - top));
    });
  }

  $effect(() => {
    void peaks;
    void current;
    void duration;
    void cssWidth;
    void height;
    draw();
  });

  function seekFromX(clientX: number) {
    if (!(duration > 0)) return;
    const r = canvas.getBoundingClientRect();
    if (r.width <= 0) return;
    const f = Math.min(1, Math.max(0, (clientX - r.left) / r.width));
    onseek(f * duration);
  }
  function onkey(e: KeyboardEvent) {
    if (!(duration > 0)) return;
    if (e.key === "ArrowRight") onseek(Math.min(duration, current + 5));
    else if (e.key === "ArrowLeft") onseek(Math.max(0, current - 5));
    else return;
    e.preventDefault();
  }
</script>

<div class="wf" bind:clientWidth={cssWidth} style:height="{height}px">
  <canvas
    bind:this={canvas}
    role="slider"
    tabindex="0"
    aria-label={label}
    aria-valuemin="0"
    aria-valuemax={Math.round(duration)}
    aria-valuenow={Math.round(current)}
    data-testid="waveform"
    onclick={(e) => seekFromX(e.clientX)}
    onkeydown={onkey}
    style:height="{height}px"
  ></canvas>
</div>

<style>
  .wf {
    width: 100%;
    min-width: 0;
  }
  canvas {
    width: 100%;
    display: block;
    cursor: pointer;
    touch-action: manipulation;
  }
</style>

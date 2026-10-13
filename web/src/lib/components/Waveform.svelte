<script lang="ts">
  import { pinPositions } from "../comments";
  import { peaksToColumns } from "../peaks";

  interface Props {
    /** Interleaved [min0,max0,...] samples, or null while loading. */
    peaks: readonly number[] | null;
    duration: number;
    current: number;
    onseek: (seconds: number) => void;
    /** Sample bit depth from the mm.peaks payload (int8 by default). */
    bits?: number;
    height?: number;
    label?: string;
    /** Timed comment markers; clicking one calls `onpin(id)`. */
    pins?: { id: string; t: number; kind: string }[];
    onpin?: (id: string) => void;
  }
  let { peaks, duration, current, onseek, bits = 8, height = 48, label = "Waveform", pins = [], onpin }: Props = $props();

  let canvas: HTMLCanvasElement;
  let cssWidth = $state(0);

  const placed = $derived(
    pinPositions(
      pins.map((p) => ({ id: p.id, at_second: p.t, kind: p.kind as "note" })),
      duration,
      cssWidth,
    ),
  );

  const pxWidth = $derived(Math.max(1, Math.floor(cssWidth * (typeof window === "undefined" ? 1 : window.devicePixelRatio || 1))));
  const cols = $derived(peaks && cssWidth > 0 ? peaksToColumns(peaks, Math.floor(peaks.length / 2), pxWidth) : []);
  const full = $derived(2 ** (Math.max(2, bits) - 1) - 1);

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
    const playedX = duration > 0 ? Math.min(w, (current / duration) * w) : 0;
    const mid = h / 2;
    const scale = (h / 2) * 0.95;
    // Scale by the format's full range so quiet tracks stay quiet.
    const ext = full;
    cols.forEach((c, x) => {
      ctx.fillStyle = x < playedX ? "#f2a65a" : "#7a6a5c";
      const top = mid - (c.max / ext) * scale;
      const bot = mid - (c.min / ext) * scale;
      ctx.fillRect(x, top, 1, Math.max(1, bot - top));
    });
  }

  $effect(() => {
    void cols;
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
  {#each placed as p (p.id)}
    <button
      type="button"
      class="pin {pins.find((x) => x.id === p.id)?.kind ?? ''}"
      style:left="{p.x}px"
      aria-label="Comment at {Math.floor(p.t / 60)}:{String(Math.floor(p.t % 60)).padStart(2, '0')}"
      data-testid="comment-pin"
      onclick={() => onpin?.(p.id)}
    ></button>
  {/each}
</div>

<style>
  .wf {
    position: relative;
    width: 100%;
    min-width: 0;
  }
  .pin {
    position: absolute;
    top: 0;
    width: 0.6rem;
    height: 0.6rem;
    margin-left: -0.3rem;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: #6ab0f2;
    cursor: pointer;
  }
  .pin.fix {
    background: #f27a6a;
  }
  .pin.keep {
    background: #7fd68a;
  }
  .pin::after {
    content: "";
    position: absolute;
    inset: -0.55rem;
  }
  canvas {
    width: 100%;
    display: block;
    cursor: pointer;
    touch-action: manipulation;
  }
</style>

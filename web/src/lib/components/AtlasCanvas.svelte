<script lang="ts">
  import { onMount } from "svelte";
  import {
    type AtlasModel,
    type AtlasPoint,
    type AxisPair,
    type Dir,
    HIT_RADIUS,
    HOME_VIEW,
    type Plot,
    type Rect,
    type View,
    axisTicks,
    buildGrid,
    buildModel,
    hitTest,
    nearestInDirection,
    panBy,
    pointLines,
    project,
    projectAll,
    rectSelect,
    zoomAt,
  } from "../atlasView";
  import { readPalette } from "../palette";

  interface Props {
    points: readonly AtlasPoint[];
    axes: AxisPair;
    /** Guide lines (identity, octave up/down) drawn for the requested-vs-got map. */
    guides?: readonly { kind: string }[];
    /** Predicted points are hidden unless this is false (D-L3-14 default). */
    hidePredicted?: boolean;
    /** Colour index highlighted by a legend click; the others are dimmed client-side. */
    focusColor?: number | null;
    selected?: ReadonlySet<string>;
    /** Touch rectangle selection: a drag draws a rectangle instead of panning. */
    selectMode?: boolean;
    onopen: (id: string) => void;
    onselect: (ids: string[], mode: "toggle" | "add" | "clear") => void;
  }
  let { points, axes, guides = [], hidePredicted = true, focusColor = null, selected = new Set<string>(), selectMode = false, onopen, onselect }: Props = $props();

  const PAD = { left: 48, top: 10, right: 14, bottom: 30 };
  const R = 5;

  let wrap: HTMLDivElement;
  let canvas: HTMLCanvasElement;
  let width = $state(320);
  let height = $state(320);
  let view = $state<View>({ ...HOME_VIEW });
  let active = $state(-1); // focused or tapped point (index into the model)
  let hover = $state(-1);
  let rect = $state<Rect | null>(null);
  let palette: string[] = [];
  let ink = { line: "", text: "", ring: "", bg: "" };
  let raf = 0;

  const model: AtlasModel = $derived(buildModel(points, { hidePredicted }));
  const plot: Plot = $derived({ left: PAD.left, top: PAD.top, width: Math.max(40, width - PAD.left - PAD.right), height: Math.max(40, height - PAD.top - PAD.bottom) });
  const proj = $derived(projectAll(model, view, plot));
  const grid = $derived(buildGrid(proj.xs, proj.ys, 32));
  const shown = $derived(active >= 0 && active < model.points.length ? active : hover >= 0 && hover < model.points.length ? hover : -1);
  const lines = $derived(shown >= 0 ? pointLines(model.points[shown]!, axes) : null);
  const tip = $derived.by(() => {
    if (shown < 0) return null;
    const x = Math.min(Math.max(proj.xs[shown]! + 12, 4), Math.max(4, width - 224));
    const y = Math.min(Math.max(proj.ys[shown]! + 12, 4), Math.max(4, height - 150));
    return { x, y };
  });
  const status = $derived(lines ? `${lines.title}, ${lines.state}, pick ${lines.pick}, ${lines.x}, ${lines.y}` : `${model.points.length} points`);

  // A new data set or a hidden active point resets the transient state.
  $effect(() => {
    if (active >= model.points.length) active = -1;
  });

  function schedule() {
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      draw();
    });
  }

  function guideLine(ctx: CanvasRenderingContext2D, kind: string) {
    // Guides live in map space of the two tempo axes: identity, octave up, octave down.
    const octave = axes.x.min && axes.x.max ? 1 / Math.log2(axes.x.max / axes.x.min) : 0;
    const shift = /up/.test(kind) ? octave : /down/.test(kind) ? -octave : 0;
    const a = project(view, plot, 0, shift);
    const b = project(view, plot, 1, 1 + shift);
    ctx.beginPath();
    ctx.moveTo(a[0], a[1]);
    ctx.lineTo(b[0], b[1]);
    ctx.stroke();
  }

  function draw() {
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const dpr = window.devicePixelRatio || 1;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);
    ctx.font = "11px system-ui, sans-serif";
    ctx.lineWidth = 1;

    ctx.save();
    ctx.beginPath();
    ctx.rect(plot.left, plot.top, plot.width, plot.height);
    ctx.clip();
    ctx.strokeStyle = ink.line;
    for (const t of axisTicks(axes.x)) {
      const [sx] = project(view, plot, t.u, 0);
      ctx.beginPath();
      ctx.moveTo(sx, plot.top);
      ctx.lineTo(sx, plot.top + plot.height);
      ctx.stroke();
    }
    for (const t of axisTicks(axes.y)) {
      const [, sy] = project(view, plot, 0, t.u);
      ctx.beginPath();
      ctx.moveTo(plot.left, sy);
      ctx.lineTo(plot.left + plot.width, sy);
      ctx.stroke();
    }
    if (guides.length) {
      ctx.setLineDash([4, 4]);
      for (const g of guides) guideLine(ctx, g.kind);
      ctx.setLineDash([]);
    }
    const sel = selected;
    for (let i = 0; i < model.points.length; i++) {
      const x = proj.xs[i]!;
      const y = proj.ys[i]!;
      if (x < plot.left - R || x > plot.left + plot.width + R || y < plot.top - R || y > plot.top + plot.height + R) continue;
      const p = model.points[i]!;
      const colour = palette[Math.min(Math.max(p.c, 0), 12)] || ink.text;
      ctx.globalAlpha = model.dim[i] || (focusColor !== null && p.c !== focusColor) ? 0.2 : 1;
      ctx.beginPath();
      ctx.arc(x, y, R, 0, Math.PI * 2);
      if (model.hollow[i]) {
        ctx.lineWidth = 1.5;
        ctx.strokeStyle = colour;
        ctx.stroke();
      } else {
        ctx.fillStyle = colour;
        ctx.fill();
      }
      if (sel.has(p.id)) {
        ctx.globalAlpha = 1;
        ctx.lineWidth = 2;
        ctx.strokeStyle = ink.ring;
        ctx.beginPath();
        ctx.arc(x, y, R + 3, 0, Math.PI * 2);
        ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;
    if (active >= 0 || hover >= 0) {
      const i = shown;
      if (i >= 0) {
        ctx.lineWidth = 2;
        ctx.strokeStyle = ink.ring;
        ctx.beginPath();
        ctx.arc(proj.xs[i]!, proj.ys[i]!, R + 6, 0, Math.PI * 2);
        ctx.stroke();
      }
    }
    if (rect) {
      ctx.lineWidth = 1;
      ctx.setLineDash([4, 3]);
      ctx.strokeStyle = ink.ring;
      ctx.strokeRect(rect.x0, rect.y0, rect.x1 - rect.x0, rect.y1 - rect.y0);
      ctx.setLineDash([]);
    }
    ctx.restore();

    ctx.fillStyle = ink.text;
    ctx.textBaseline = "top";
    ctx.textAlign = "center";
    for (const t of axisTicks(axes.x)) {
      const [sx] = project(view, plot, t.u, 0);
      if (sx >= plot.left - 2 && sx <= plot.left + plot.width + 2) ctx.fillText(t.label, sx, plot.top + plot.height + 6, 60);
    }
    ctx.textAlign = "right";
    ctx.textBaseline = "middle";
    for (const t of axisTicks(axes.y)) {
      const [, sy] = project(view, plot, 0, t.u);
      if (sy >= plot.top - 2 && sy <= plot.top + plot.height + 2) ctx.fillText(t.label, plot.left - 6, sy, PAD.left - 8);
    }
  }

  function readInk() {
    const cs = getComputedStyle(canvas);
    ink = { line: cs.getPropertyValue("--line").trim(), text: cs.getPropertyValue("--text-2").trim(), ring: cs.getPropertyValue("--focus").trim(), bg: cs.getPropertyValue("--bg").trim() };
    palette = readPalette(canvas);
  }

  function resize() {
    const r = wrap.getBoundingClientRect();
    width = Math.max(120, Math.round(r.width));
    height = Math.max(200, Math.round(r.height));
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    schedule();
  }

  onMount(() => {
    readInk();
    resize();
    const ro = new ResizeObserver(resize);
    ro.observe(wrap);
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = canvas.getBoundingClientRect();
      view = zoomAt(view, plot, Math.exp(-e.deltaY * 0.0015), e.clientX - r.left, e.clientY - r.top);
    };
    canvas.addEventListener("wheel", wheel, { passive: false });
    return () => {
      ro.disconnect();
      canvas.removeEventListener("wheel", wheel);
      if (raf) cancelAnimationFrame(raf);
    };
  });

  // Redraw on any reactive input.
  $effect(() => {
    void [proj, view, active, hover, rect, selected, focusColor, axes, guides, plot];
    schedule();
  });

  // --- pointer: pan, pinch, tap, rectangle -------------------------------------------------------------

  const ptrs: Record<number, { x: number; y: number }> = {};
  const ptrList = () => Object.values(ptrs);
  let gesture: { kind: "pan" | "rect" | "pinch" | "none"; sx: number; sy: number; t: number; moved: boolean; dist: number; touch: boolean } | null = null;

  function local(e: PointerEvent): { x: number; y: number } {
    const r = canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  function pick(x: number, y: number, touch: boolean): number {
    return hitTest(grid, proj.xs, proj.ys, x, y, touch ? HIT_RADIUS.touch : HIT_RADIUS.mouse);
  }

  function ondown(e: PointerEvent) {
    const p = local(e);
    ptrs[e.pointerId] = p;
    canvas.setPointerCapture(e.pointerId);
    const touch = e.pointerType !== "mouse";
    if (ptrList().length === 2) {
      const [a, b] = ptrList();
      gesture = { kind: "pinch", sx: p.x, sy: p.y, t: 0, moved: true, dist: Math.hypot(a!.x - b!.x, a!.y - b!.y), touch };
      rect = null;
      return;
    }
    const wantRect = e.shiftKey || selectMode;
    gesture = { kind: wantRect ? "rect" : "pan", sx: p.x, sy: p.y, t: performance.now(), moved: false, dist: 0, touch };
  }

  function onmove(e: PointerEvent) {
    const p = local(e);
    const prev = ptrs[e.pointerId];
    if (!gesture || !prev) {
      if (e.pointerType === "mouse") hover = pick(p.x, p.y, false);
      return;
    }
    ptrs[e.pointerId] = p;
    if (gesture.kind === "pinch" && ptrList().length >= 2) {
      const [a, b] = ptrList();
      const d = Math.hypot(a!.x - b!.x, a!.y - b!.y);
      if (gesture.dist > 0 && d > 0) view = zoomAt(view, plot, d / gesture.dist, (a!.x + b!.x) / 2, (a!.y + b!.y) / 2);
      const k = ptrList().length;
      view = panBy(view, plot, (p.x - prev.x) / k, (p.y - prev.y) / k);
      gesture.dist = d;
      return;
    }
    if (Math.hypot(p.x - gesture.sx, p.y - gesture.sy) > 8) gesture.moved = true;
    if (!gesture.moved) return;
    if (gesture.kind === "rect") rect = { x0: gesture.sx, y0: gesture.sy, x1: p.x, y1: p.y };
    else if (gesture.kind === "pan") view = panBy(view, plot, p.x - prev.x, p.y - prev.y);
  }

  function onup(e: PointerEvent) {
    const p = local(e);
    delete ptrs[e.pointerId];
    const g = gesture;
    if (ptrList().length > 0) {
      if (g) g.kind = "none";
      return;
    }
    gesture = null;
    if (!g || g.kind === "none" || g.kind === "pinch") return;
    if (g.kind === "rect" && g.moved && rect) {
      const idx = rectSelect(proj.xs, proj.ys, rect);
      rect = null;
      onselect(idx.map((i) => model.points[i]!.id), "add");
      return;
    }
    rect = null;
    if (!g.moved) {
      // A tap: nearest point within the hit radius (22 px on touch), else dismiss.
      const i = pick(p.x, p.y, g.touch);
      active = i;
      if (i >= 0) canvas.focus({ preventScroll: true });
    }
  }

  function oncancel(e: PointerEvent) {
    delete ptrs[e.pointerId];
    gesture = null;
    rect = null;
  }

  function onkeydown(e: KeyboardEvent) {
    const dirs: Record<string, Dir> = { ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" };
    const d = dirs[e.key];
    const n = model.points.length;
    if (d) {
      e.preventDefault();
      if (n === 0) return;
      if (active < 0) active = 0;
      else {
        const j = nearestInDirection(model, active, d);
        if (j >= 0) active = j;
      }
      reveal(active);
    } else if (e.key === "Enter" && active >= 0) {
      e.preventDefault();
      onopen(model.points[active]!.id);
    } else if (e.key === " " && active >= 0) {
      e.preventDefault();
      onselect([model.points[active]!.id], "toggle");
    } else if (e.key === "Escape") {
      active = -1;
      onselect([], "clear");
    } else if (e.key === "+" || e.key === "=") view = zoomAt(view, plot, 1.5, plot.left + plot.width / 2, plot.top + plot.height / 2);
    else if (e.key === "-") view = zoomAt(view, plot, 1 / 1.5, plot.left + plot.width / 2, plot.top + plot.height / 2);
    else if (e.key === "0") view = { ...HOME_VIEW };
  }

  /** Pan so the keyboard-focused point stays in the window. */
  function reveal(i: number) {
    const x = proj.xs[i]!;
    const y = proj.ys[i]!;
    const m = 24;
    let dx = 0;
    let dy = 0;
    if (x < plot.left + m) dx = plot.left + m - x;
    else if (x > plot.left + plot.width - m) dx = plot.left + plot.width - m - x;
    if (y < plot.top + m) dy = plot.top + m - y;
    else if (y > plot.top + plot.height - m) dy = plot.top + plot.height - m - y;
    if (dx || dy) view = panBy(view, plot, dx, dy);
  }

  function zoomBy(f: number) {
    view = zoomAt(view, plot, f, plot.left + plot.width / 2, plot.top + plot.height / 2);
  }
</script>

<div class="wrap" bind:this={wrap}>
  <canvas
    bind:this={canvas}
    data-testid="atlas-canvas"
    tabindex="0"
    aria-label="Song Atlas map. Arrow keys move between points, Enter opens the Song, Space selects it, plus and minus zoom, Escape clears."
    aria-describedby="atlas-canvas-status"
    style:width="{width}px"
    style:height="{height}px"
    onpointerdown={ondown}
    onpointermove={onmove}
    onpointerup={onup}
    onpointercancel={oncancel}
    onpointerleave={() => (hover = -1)}
    onkeydown={onkeydown}
    onfocus={() => {
      if (active < 0 && model.points.length > 0) active = 0;
    }}
  ></canvas>
  <p id="atlas-canvas-status" class="sr" aria-live="polite">{status}</p>
  <div class="zoom" role="group" aria-label="Zoom">
    <button type="button" aria-label="Zoom in" onclick={() => zoomBy(1.5)}>+</button>
    <button type="button" aria-label="Zoom out" onclick={() => zoomBy(1 / 1.5)}>−</button>
    <button type="button" aria-label="Reset view" onclick={() => (view = { ...HOME_VIEW })} disabled={view.zoom === 1}>1×</button>
  </div>
  {#if lines && tip && shown >= 0}
    <div class="tip" data-testid="atlas-tooltip" style:left="{tip.x}px" style:top="{tip.y}px">
      <strong>{lines.title}</strong>
      <span>{lines.state} · pick {lines.pick}</span>
      <span>x {lines.x}</span>
      <span>y {lines.y}</span>
      {#if active >= 0}
        <span class="acts">
          <button type="button" onclick={() => onopen(model.points[active]!.id)}>Open</button>
          <button type="button" onclick={() => onselect([model.points[active]!.id], "toggle")}>{selected.has(model.points[active]!.id) ? "Deselect" : "Select"}</button>
        </span>
      {/if}
    </div>
  {/if}
  {#if model.hiddenPredicted > 0}
    <p class="note" data-testid="atlas-hidden-note">{model.hiddenPredicted} predicted {model.hiddenPredicted === 1 ? "point is" : "points are"} hidden.</p>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
    width: 100%;
    height: min(70vh, 640px);
    min-height: 280px;
    background: var(--bg-sunken);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  canvas {
    display: block;
    touch-action: none;
    cursor: crosshair;
    user-select: none;
    -webkit-user-select: none;
  }
  canvas:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    margin: 0;
  }
  .zoom {
    position: absolute;
    right: var(--sp-2);
    top: var(--sp-2);
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
  }
  .zoom button {
    min-width: var(--hit);
    min-height: var(--hit);
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-sm);
    font-size: var(--fs-md);
  }
  .zoom button:disabled {
    color: var(--text-3);
  }
  .tip {
    position: absolute;
    width: 212px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--sp-2) var(--sp-3);
    background: var(--surface-2);
    color: var(--text);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    font-size: var(--fs-sm);
    box-shadow: var(--shadow-sheet);
    z-index: 1;
  }
  .tip span {
    color: var(--text-2);
    overflow-wrap: anywhere;
  }
  .acts {
    display: flex;
    gap: var(--sp-2);
    margin-top: var(--sp-1);
  }
  .acts button {
    min-height: var(--hit);
    flex: 1;
    background: var(--accent-soft);
    color: var(--text);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-sm);
  }
  .note {
    position: absolute;
    left: var(--sp-2);
    bottom: var(--sp-1);
    margin: 0;
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
</style>

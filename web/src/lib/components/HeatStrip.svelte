<script lang="ts">
  // The attention heat strip under the waveform (M5-W1, C256, C262): 120 bins of how the operator listened, three thin bands
  // (heard, went back, skipped) and up to three hotspot chips that jump with jumpTo.
  import { api } from "../../api/client";
  import type { ListenHeat } from "../heatApi";
  import { chipText, heatSummary, loadHeat, strips } from "../heatView";
  import { jumpTo } from "../jump";
  import type { Load } from "../songPrView";

  interface Props {
    trackId: string;
    /** Seconds; the strip spans the take. */
    duration?: number;
    /** Pre-loaded heat (server-side rendering in unit tests; the strip otherwise fetches it). */
    initial?: Load<ListenHeat>;
    /** Reference and lossy takes carry no heat worth a request (browsers log every non-2xx as a console error). */
    enabled?: boolean;
  }
  let { trackId, initial, enabled = true }: Props = $props();

  // svelte-ignore state_referenced_locally
  let heat = $state<Load<ListenHeat>>(initial ?? { state: "loading" });

  $effect(() => {
    if (initial || !enabled) return;
    const mine = trackId;
    heat = { state: "loading" };
    void loadHeat(api, mine).then((r) => {
      if (mine === trackId) heat = r;
    });
  });

  const bands = $derived(heat.state === "ready" ? strips(heat.value) : null);
  const bins = $derived(heat.state === "ready" ? Math.max(1, heat.value.bins) : 1);
  const spots = $derived(heat.state === "ready" ? (heat.value.hotspots ?? []) : []);
</script>

{#if enabled || initial}
<div class="heat" data-testid="heat-strip" data-track={trackId} data-state={heat.state}>
  {#if heat.state === "ready" && bands}
    <svg class="bands" viewBox="0 0 {bins} 3" preserveAspectRatio="none" role="img" aria-label={heatSummary(heat.value)} data-testid="heat-bands">
      {#each bands.heard as v, i (i)}
        {#if v > 0}<rect x={i} y="0" width="1" height="1.6" class="heard" fill-opacity={0.15 + 0.85 * v} />{/if}
      {/each}
      {#each bands.hot as v, i (i)}
        {#if v > 0}<rect x={i} y="1.6" width="1" height="0.7" class="hot" fill-opacity={0.3 + 0.7 * v} />{/if}
      {/each}
      {#each bands.skips as v, i (i)}
        {#if v > 0}<rect x={i} y="2.3" width="1" height="0.7" class="skip" fill-opacity={0.3 + 0.7 * v} />{/if}
      {/each}
    </svg>
    {#if heat.value.sessions === 0}
      <span class="sr" data-testid="heat-empty">No listening recorded yet.</span>
    {/if}
    {#if spots.length > 0}
      <ul class="chips" data-testid="heat-hotspots" aria-label="Hotspots where you went back">
        {#each spots as h (h.from_s)}
          <li><button type="button" class="chip" data-testid="heat-hotspot" data-from={h.from_s} onclick={() => jumpTo(trackId, h.from_s)}>{chipText(h)}</button></li>
        {/each}
      </ul>
    {/if}
  {:else if heat.state === "unavailable"}
    <span class="sr" data-testid="heat-unavailable">Listening heat is not available yet.</span>
  {:else if heat.state === "error"}
    <span class="sr" data-testid="heat-error">{heat.message}</span>
  {/if}
</div>
{/if}

<style>
  /* The strip adds no height of its own: its bands overlay the bottom edge of the waveform above (the phone's judging controls
     must not move down under the fixed bars), and the sr-only states never take space. Only hotspot chips are in flow. */
  .heat {
    position: relative;
    min-width: 0;
    max-width: 100%;
    height: 0;
  }
  .heat:has(.chips) {
    height: auto;
    padding-top: 0.3rem;
  }
  .bands {
    position: absolute;
    left: 0;
    right: 0;
    top: -0.6rem;
    width: 100%;
    height: 0.45rem;
    pointer-events: none;
    border-radius: var(--r-sm);
    background: color-mix(in srgb, var(--bg-sunken) 70%, transparent);
  }
  .heard {
    fill: var(--accent);
  }
  .hot {
    fill: #e8806a;
  }
  .skip {
    fill: #7a8fa6;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .chips {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .chip {
    min-height: var(--hit);
    min-width: var(--hit);
    max-width: 100%;
    overflow-wrap: anywhere;
    font-variant-numeric: tabular-nums;
  }
</style>

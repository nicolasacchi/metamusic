<script lang="ts">
  // The listening check of a take (M5b C347.1): what speech recognition and pitch tracking heard on the separated vocals. These are
  // suspicions from a recogniser that can mishear sung words, never verdicts: the blind spots are always shown.
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { adherenceLine, canAnalyze, cerBars, loadsTierB, measuredNote, modelsLine, overallLine, stateText } from "../tierbView";
  import AnalyzeButton from "./AnalyzeButton.svelte";

  type TierB = components["schemas"]["TierB"];

  interface Props {
    trackId: string;
    /** The dossier (EarsPanel's); null while it loads or when there is none. */
    dossier: { tier: string; flags: readonly { code: string }[] } | null;
    /** The dossier request failed (not "no analysis"): the panel cannot say whether a check exists. */
    failed?: boolean;
    go: (to: string) => void;
  }
  let { trackId, dossier, failed = false, go }: Props = $props();

  let data = $state<TierB | null>(null);
  let checkState = $state("");
  let loading = $state(true);
  let message = $state("");

  $effect(() => {
    const mine = trackId;
    const d = dossier;
    data = null;
    message = "";
    if (!d) {
      checkState = "";
      loading = true;
      return;
    }
    // get_track_tier_b answers 409 for a take with no check yet: the dossier says whether there is one, so none is not asked for
    if (!loadsTierB(d)) {
      checkState = "none";
      loading = false;
      return;
    }
    loading = true;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/tier-b", { params: { path: { id: mine } } });
        if (mine !== trackId) return;
        if (r.data) {
          data = r.data;
          checkState = r.data.state;
        } else {
          const s = (r.error?.details as { state?: unknown } | undefined)?.state;
          checkState = typeof s === "string" ? s : "none";
          if (r.response.status !== 409) message = r.error?.message ?? "Could not load the listening check.";
        }
      } catch {
        if (mine === trackId) message = "Failed to load the listening check.";
      } finally {
        if (mine === trackId) loading = false;
      }
    })();
  });

  const bars = $derived(data ? cerBars(data) : []);
</script>

<section class="card" aria-label="Listening check" data-testid="tierb-panel">
  <h2>Listening check</h2>
  {#if failed && !dossier}
    <p class="muted" role="status" data-testid="tierb-failed">Could not tell whether this take has a listening check.</p>
  {:else if loading}
    <p class="muted">Loading…</p>
  {:else if message}
    <p class="muted" role="status">{message}</p>
  {:else if data}
    <p data-testid="tierb-state" data-state={checkState}><strong>{stateText(checkState)}</strong> · language {data.language}{data.forced ? " (from the lyrics)" : " (detected)"}</p>
    {#if data.code}<p class="muted" data-testid="tierb-code">Code {data.code}</p>{/if}
    {#if bars.length}
      <h3>Diction per section (characters wrong, worst first)</h3>
      <ul class="bars" data-testid="tierb-bars">
        {#each bars as b (b.label)}
          <li data-testid="tierb-bar">
            <span class="name">{b.label}</span>
            <span class="track" role="img" aria-label="{b.text} of characters wrong"><i style:width="{b.pct}%"></i></span>
            <span class="num">{b.text}</span>
          </li>
        {/each}
      </ul>
      <p class="muted">{overallLine(data)}</p>
    {/if}
    <p class="muted" data-testid="tierb-measured">{measuredNote(data)}</p>
    <p data-testid="tierb-adherence">{adherenceLine(data)}</p>
    <p class="muted small" data-testid="tierb-models">{modelsLine(data)}</p>
    {#if data.blind_spots.length}
      <h3>What this cannot hear</h3>
      <ul class="blind">
        {#each data.blind_spots as b (b)}<li>{b}</li>{/each}
      </ul>
    {/if}
    {#if canAnalyze(checkState)}<AnalyzeButton {trackId} {go} />{/if}
  {:else}
    <p class="muted" data-testid="tierb-state" data-state={checkState}>{stateText(checkState)}</p>
    {#if canAnalyze(checkState)}<AnalyzeButton {trackId} {go} />{/if}
  {/if}
</section>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  h3 {
    margin: 0.6rem 0 0.2rem;
    font-size: 0.9rem;
  }
  p {
    margin: 0.3rem 0;
    overflow-wrap: anywhere;
  }
  .small {
    font-size: 0.85rem;
  }
  .bars {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.25rem;
  }
  .bars li {
    display: grid;
    grid-template-columns: minmax(4rem, 8rem) 1fr 3.5rem;
    gap: 0.5rem;
    align-items: center;
  }
  .name {
    overflow-wrap: anywhere;
  }
  .track {
    display: block;
    height: 0.6rem;
    background: #2a241f;
    border-radius: 0.3rem;
    overflow: hidden;
  }
  .track i {
    display: block;
    height: 100%;
    background: #f2a65a;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .blind {
    margin: 0;
    padding-left: 1.1rem;
    font-size: 0.85rem;
    opacity: 0.8;
  }
</style>

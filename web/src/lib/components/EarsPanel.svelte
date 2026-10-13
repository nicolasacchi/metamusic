<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { earsPanel, type EarsModel } from "../earsView";
  import Spectrogram from "./Spectrogram.svelte";

  interface Props {
    trackId: string;
  }
  let { trackId }: Props = $props();

  let model = $state<EarsModel | null>(null);
  let message = $state("");
  let loading = $state(true);

  $effect(() => {
    const mine = trackId;
    model = null;
    message = "";
    loading = true;
    void (async () => {
      try {
        const d = await api.GET("/api/tracks/{id}/analysis", { params: { path: { id: mine } } });
        if (mine !== trackId) return;
        if (!d.data) {
          message = d.error?.message ?? "No analysis yet";
          return;
        }
        let spec: components["schemas"]["SpectrogramView"] | null = null;
        if (d.data.spectrogram.available) {
          const s = await api.GET("/api/tracks/{id}/spectrogram", { params: { path: { id: mine }, query: { inline: false } } });
          spec = s.data ?? null;
        }
        if (mine === trackId) model = earsPanel(d.data, spec);
      } catch {
        if (mine === trackId) message = "Failed to load the analysis";
      } finally {
        if (mine === trackId) loading = false;
      }
    })();
  });
</script>

<section class="card" aria-label="Ears" data-testid="ears-panel">
  <h2>Ears</h2>
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if message}
    <p class="muted" role="status">{message}</p>
  {:else if model}
    <p class="muted">{model.summary}</p>
    {#if model.notice}<p role="status">{model.notice}</p>{/if}
    <h3>Flags</h3>
    {#if model.flags.length === 0}
      <p class="muted">None.</p>
    {:else}
      <ul class="flags">
        {#each model.flags as f (f.code)}
          <li class:warn={f.severity === "warn"} data-testid="ears-flag"><strong>{f.label}</strong>{f.detail ? ` — ${f.detail}` : ""} <span class="muted">({f.severity})</span></li>
        {/each}
      </ul>
    {/if}
    <h3>Metrics</h3>
    <dl>
      {#each model.metrics as m (m.label)}
        <dt>{m.label}</dt>
        <dd>{m.value}</dd>
      {/each}
    </dl>
    {#if model.spectrogram}
      <Spectrogram {trackId} url={model.spectrogram.url} plot={model.spectrogram.plot} width={model.spectrogram.width} height={model.spectrogram.height} />
    {/if}
    <h3>What this cannot hear</h3>
    <ul class="blind">
      {#each model.blindSpots as b (b)}<li>{b}</li>{/each}
    </ul>
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
  ul {
    margin: 0;
    padding-left: 1.1rem;
  }
  li.warn strong {
    color: #f2a65a;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.1rem 0.8rem;
    margin: 0;
  }
  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }
  .blind {
    font-size: 0.85rem;
    opacity: 0.8;
  }
</style>

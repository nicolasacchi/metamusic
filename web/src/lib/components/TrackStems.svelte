<script lang="ts">
  // Stems of a take (E2-W5): play and download each stem through the signed media links. Renders nothing for a take without stems.
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { player } from "../player.svelte";

  type StemLink = components["schemas"]["StemLink"];
  interface Props {
    trackId: string;
    stems: readonly components["schemas"]["StemInfo"][];
  }
  let { trackId, stems }: Props = $props();

  let links = $state<StemLink[]>([]);
  let error = $state("");
  let audios = $state<Record<string, HTMLAudioElement | undefined>>({});

  $effect(() => {
    const id = trackId;
    if (stems.length === 0) return;
    links = [];
    error = "";
    void api
      .GET("/api/tracks/{id}/media", { params: { path: { id } } })
      .then((r) => {
        if (id !== trackId) return;
        if (r.data) links = r.data.stems ?? [];
        else error = r.error?.message || "Could not load the stems.";
      })
      .catch(() => {
        if (id === trackId) error = "Could not load the stems.";
      });
  });

  const label = (n: string) => n.replaceAll("_", " ");

  // One thing at a time: starting a stem pauses the take player and the other stems.
  function onplay(name: string) {
    if (player.state.playing) player.toggle();
    for (const [k, a] of Object.entries(audios)) if (k !== name) a?.pause();
  }
</script>

{#if stems.length > 0}
  <section class="card" data-testid="stems-panel">
    <h2>Stems</h2>
    {#if error}<p role="alert">{error}</p>{/if}
    <ul>
      {#each links as s (s.name)}
        <li data-testid="stem-{s.name}">
          <span class="name">{label(s.name)}</span>
          <audio controls preload="none" src={s.url} bind:this={audios[s.name]} onplay={() => onplay(s.name)}></audio>
          <a class="dl" href={s.url} download data-testid="stem-download-{s.name}">Download</a>
        </li>
      {/each}
    </ul>
  </section>
{/if}

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
    min-width: 0;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem 0.6rem;
    min-width: 0;
    padding: 0.2rem 0;
  }
  .name {
    flex: 1 1 100%;
    text-transform: capitalize;
  }
  audio {
    flex: 1 1 12rem;
    min-width: 0;
    max-width: 100%;
    min-height: 44px;
  }
  .dl {
    display: inline-flex;
    align-items: center;
    min-height: 44px;
    padding: 0 0.7rem;
    border: 1px solid currentColor;
    border-radius: 0.3rem;
    text-decoration: none;
  }
</style>

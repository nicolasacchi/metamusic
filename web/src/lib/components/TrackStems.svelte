<script lang="ts">
  // Stems of a take (E2-W5): play and download each stem through the signed media links. Renders nothing for a take without stems.
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { fetchClip } from "../../api/media";
  import { getAudioContext } from "../audioContext";
  import type { Lease } from "../audioFocus";
  import { player } from "../player.svelte";
  import { onDestroy } from "svelte";

  type StemLink = components["schemas"]["StemLink"];
  interface Props {
    trackId: string;
    stems: readonly components["schemas"]["StemInfo"][];
  }
  let { trackId, stems }: Props = $props();

  let links = $state<StemLink[]>([]);
  let error = $state("");
  // Stems play through Web Audio on the shared context, never a second audio element (one element app-wide, iOS).
  // Starting one borrows the output from the take player (audio focus) and stops any other stem.
  let playing = $state<string | null>(null);
  let loading = $state<string | null>(null);
  const buffers: Record<string, AudioBuffer | undefined> = {};
  let source: AudioBufferSourceNode | null = null;
  let lease: Lease | null = null;
  let token = 0;

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

  function stop() {
    token++;
    const src = source;
    source = null;
    if (src) {
      src.onended = null;
      try {
        src.stop();
      } catch {
        // already stopped
      }
      src.disconnect();
    }
    playing = null;
    loading = null;
    lease?.release();
    lease = null;
  }
  onDestroy(stop);
  $effect(() => {
    void trackId;
    for (const k of Object.keys(buffers)) delete buffers[k];
    return stop;
  });

  async function toggle(l: StemLink) {
    if (playing === l.name || loading === l.name) {
      stop();
      return;
    }
    stop();
    const my = token;
    let ctx: AudioContext;
    try {
      ctx = getAudioContext();
    } catch (e) {
      error = e instanceof Error ? e.message : "Playback is not available here.";
      return;
    }
    void ctx.resume().catch(() => {}); // inside the click, before any await (iOS)
    lease = player.acquireFocus("stems", { handlers: { play: () => void toggle(l), pause: () => stop(), playing: () => playing !== null }, onEnd: () => void ctx.suspend().catch(() => {}) });
    loading = l.name;
    error = "";
    try {
      let buf = buffers[l.name];
      if (!buf) {
        buf = await ctx.decodeAudioData(await fetchClip(l.url));
        buffers[l.name] = buf;
      }
      if (my !== token) return;
      const src = ctx.createBufferSource();
      src.buffer = buf;
      src.connect(ctx.destination);
      src.onended = () => {
        if (my === token) stop();
      };
      src.start();
      source = src;
      playing = l.name;
      loading = null;
      lease?.changed();
    } catch (e) {
      if (my !== token) return;
      stop();
      error = e instanceof Error ? e.message : "Could not play the stem.";
    }
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
          <button type="button" class="play" aria-pressed={playing === s.name} onclick={() => void toggle(s)} data-testid="stem-play-{s.name}">
            {playing === s.name ? "Stop" : loading === s.name ? "Loading…" : "Play"}
          </button>
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
    text-transform: capitalize;
  }
  .name {
    flex: 1 1 8rem;
  }
  .play {
    min-height: 44px;
    min-width: 44px;
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

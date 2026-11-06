<script lang="ts">
  // Timed lyrics (LRC) synced to the playhead (E2-W5). Renders nothing for a take without an LRC or when it cannot be read.
  import { api } from "../../api/client";
  import { fetchClip } from "../../api/media";
  import { lrcActive, parseLrc, type LrcLine } from "../lrc";
  import { fmt } from "../time";

  interface Props {
    trackId: string;
    /** Whether the take has an lrc artifact (get_track lrc present). */
    has: boolean;
    /** Playback position in seconds, or null when this take is not the one playing. */
    current: number | null;
    onseek: (seconds: number) => void;
  }
  let { trackId, has, current, onseek }: Props = $props();

  let lines = $state<LrcLine[]>([]);
  const active = $derived(current === null ? -1 : lrcActive(lines, current));
  let box = $state<HTMLElement | undefined>();

  $effect(() => {
    const id = trackId;
    lines = [];
    if (!has) return;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/media", { params: { path: { id } } });
        const url = r.data?.lrc?.url;
        if (!url) return;
        const text = new TextDecoder().decode(await fetchClip(url));
        if (id === trackId) lines = parseLrc(text);
      } catch {
        // No synced lyrics is not an error for the page.
      }
    })();
  });

  // Keep the sung line in view inside the panel only (never scroll the page).
  $effect(() => {
    if (active < 0 || !box) return;
    const el = box.querySelector<HTMLElement>(`[data-i="${active}"]`);
    if (el && box.scrollHeight > box.clientHeight) box.scrollTop = el.offsetTop - box.clientHeight / 2;
  });
</script>

{#if lines.length > 0}
  <section class="card" data-testid="lrc-panel">
    <h2>Synced lyrics</h2>
    <div class="lines" bind:this={box} tabindex="-1">
      {#each lines as l, i (i)}
        <button type="button" class="line" class:active={i === active} data-i={i} data-active={i === active ? "true" : "false"} aria-current={i === active ? "true" : undefined} onclick={() => onseek(l.t)} title="Play from {fmt(l.t)}">
          {l.text || "…"}
        </button>
      {/each}
    </div>
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
  .lines {
    max-height: 18rem;
    overflow-y: auto;
    position: relative;
    display: flex;
    flex-direction: column;
  }
  .line {
    text-align: left;
    background: none;
    border: 0;
    border-left: 3px solid transparent;
    color: inherit;
    font: inherit;
    min-height: 44px;
    padding: 0.2rem 0.6rem;
    overflow-wrap: anywhere;
    opacity: 0.7;
  }
  .line.active {
    border-left-color: #f2a65a;
    background: #231d17;
    opacity: 1;
  }
</style>

<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { player } from "../lib/player.svelte";
  import { fmt, parseT } from "../lib/time";
  import Waveform from "../lib/components/Waveform.svelte";
  import LyricsPanel from "../lib/components/LyricsPanel.svelte";
  import MetricsCard from "../lib/components/MetricsCard.svelte";

  type Track = components["schemas"]["TrackDetail"];
  interface Props {
    id: string;
    /** `?t=` deep link ("72" or "1:12"). */
    t?: string;
  }
  let { id, t = "" }: Props = $props();

  let track = $state<Track | null>(null);
  let error = $state("");
  let overview = $state<number[] | null>(null);
  let detail = $state<number[] | null>(null);
  let bits = $state(8);
  let width = $state(0);

  const isCurrent = $derived(player.state.trackId === id);
  const position = $derived(isCurrent ? player.state.current : 0);
  const duration = $derived(isCurrent && player.state.duration > 0 ? player.state.duration : (track?.duration_s ?? 0));
  // The detail level is only worth fetching when the canvas has more device pixels than overview columns.
  const wantDetail = $derived(width * (typeof window === "undefined" ? 1 : window.devicePixelRatio || 1) > 2048);
  const peaks = $derived(wantDetail && detail ? detail : overview);

  // Load the track (and its overview peaks) whenever the route id changes.
  $effect(() => {
    const mine = id;
    track = null;
    error = "";
    overview = null;
    detail = null;
    void (async () => {
      try {
        const [tr, pk] = await Promise.all([
          api.GET("/api/tracks/{id}", { params: { path: { id: mine } } }),
          api.GET("/api/tracks/{id}/peaks", { params: { path: { id: mine }, query: { level: "overview" } } }),
        ]);
        if (mine !== id) return;
        if (!tr.data) {
          error = tr.response.status === 404 ? "Track not found" : (tr.error?.message ?? "Failed to load track");
          return;
        }
        track = tr.data;
        overview = pk.data?.peaks.data ?? null;
        bits = pk.data?.peaks.bits ?? 8;
        const start = parseT(t);
        // Deep link: load paused; the player applies the seek in loadedmetadata (or at once if already current).
        if (start !== null) player.load(mine, [mine], { title: tr.data.title, startAt: start, autoplay: false });
      } catch {
        if (mine === id) error = "Failed to load track";
      }
    })();
  });

  $effect(() => {
    if (!wantDetail || detail) return;
    const mine = id;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/peaks", { params: { path: { id: mine }, query: { level: "detail" } } });
        if (mine === id && r.data) {
          detail = r.data.peaks.data;
          bits = r.data.peaks.bits;
        }
      } catch {
        /* the overview stays */
      }
    })();
  });

  function play(startAt?: number) {
    if (!track) return;
    if (isCurrent && startAt === undefined) {
      player.toggle();
      return;
    }
    if (isCurrent) {
      player.seek(startAt ?? 0);
      if (!player.state.playing) player.toggle();
      return;
    }
    player.play(id, [id], { title: track.title, ...(startAt !== undefined ? { startAt } : {}) });
  }
  function seek(s: number) {
    if (isCurrent) player.seek(s);
    else play(s);
  }
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a href="/">Back to the library</a></p>
{:else if !track}
  <p class="muted">Loading…</p>
{:else}
  <article>
    <h1 data-testid="track-title">{track.title}</h1>
    <p class="meta muted">
      {fmt(track.duration_s)} · recorded {track.recorded_at.slice(0, 10)} · {track.status} · seed {track.recipe.seed}
      {#if track.bpm_declared} · {track.bpm_declared} BPM{/if}
    </p>

    <div class="controls">
      <button type="button" data-testid="track-play" onclick={() => play()}>
        {isCurrent && player.state.playing ? "Pause" : "Play"}
      </button>
      <span class="time" data-testid="track-time">{fmt(position)} / {fmt(duration)}</span>
    </div>

    <div class="wave" bind:clientWidth={width}>
      <Waveform {peaks} {duration} current={position} onseek={seek} {bits} height={96} label="Track waveform" />
    </div>

    <div class="terms">
      {#each track.style_terms as term (term)}<span class="chip">{term}</span>{/each}
    </div>

    <div class="grid">
      <section>
        <h2>Lyrics</h2>
        <LyricsPanel sections={track.sections} timed={track.timed} current={isCurrent ? player.state.current : null} onseek={seek} />
      </section>
      <aside>
        <MetricsCard analysis={track.analysis} truncated={track.truncated} />
        <section class="card">
          <h2>Recipe</h2>
          <p class="style" data-testid="recipe-style">{track.recipe.style}</p>
          <p class="muted">request {track.recipe.request_id}</p>
        </section>
        <section class="card">
          <h2>Lineage</h2>
          <p class="muted">
            generation {track.lineage.generation}{track.lineage.inferred ? " (inferred)" : ""}
            {#if track.lineage.parent_track_id}
              · <a href="/tracks/{track.lineage.parent_track_id}">parent</a>
            {/if}
            {#if track.lineage.root_track_id !== track.id}
              · <a href="/tracks/{track.lineage.root_track_id}">root</a>
            {/if}
          </p>
          {#if track.lineage.siblings.length > 0}
            <ul>
              {#each track.lineage.siblings as s (s.id)}
                <li><a href="/tracks/{s.id}">{s.title}</a> <span class="muted">seed {s.seed}</span></li>
              {/each}
            </ul>
          {/if}
        </section>
      </aside>
    </div>
  </article>
{/if}

<style>
  h1 {
    margin-bottom: 0.2rem;
  }
  .meta {
    margin-top: 0;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin: 0.6rem 0;
  }
  .time {
    font-variant-numeric: tabular-nums;
  }
  .wave {
    width: 100%;
  }
  .terms {
    margin: 0.6rem 0;
  }
  .chip {
    display: inline-block;
    font-size: 0.75rem;
    padding: 0 0.5rem;
    margin: 0 0.25rem 0.2rem 0;
    border-radius: 0.6rem;
    background: #3a322b;
  }
  .grid {
    display: grid;
    gap: 1.5rem;
    grid-template-columns: minmax(0, 2fr) minmax(16rem, 1fr);
  }
  @media (max-width: 760px) {
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  aside {
    display: grid;
    gap: 1rem;
    align-content: start;
  }
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  .card h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  .style {
    margin: 0;
    overflow-wrap: anywhere;
  }
  ul {
    margin: 0;
    padding-left: 1.1rem;
  }
</style>

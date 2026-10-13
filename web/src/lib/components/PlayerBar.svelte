<script lang="ts">
  import { api } from "../../api/client";
  import type { Player } from "../player.svelte";
  import { fmt } from "../time";
  import { judge } from "../judge.svelte";
  import CommentComposer from "./CommentComposer.svelte";
  import StarRating from "./StarRating.svelte";
  import StatusSelect from "./StatusSelect.svelte";
  import Waveform from "./Waveform.svelte";

  let { player }: { player: Player } = $props();

  let peaks = $state<number[] | null>(null);
  let bits = $state(8);
  let peaksFor = "";
  let commenting = $state(false);
  const trackId = $derived(player.state.trackId);
  const j = $derived(trackId ? judge.get(trackId) : undefined);

  // Seed the judging state for the current track unless a page already did.
  $effect(() => {
    const id = trackId;
    commenting = false;
    if (!id || judge.get(id)) return;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}", { params: { path: { id } } });
        if (r.data && !judge.get(id)) {
          judge.seed(id, {
            stars: r.data.rating.human?.stars ?? null,
            verdict: r.data.rating.human?.verdict ?? null,
            critic: r.data.rating.critic ? { stars: r.data.rating.critic.stars, verdict: r.data.rating.critic.verdict } : null,
            status: r.data.status,
            tags: r.data.tags,
          });
        }
      } catch {
        /* the bar works without judging controls */
      }
    })();
  });

  $effect(() => {
    const id = player.state.trackId;
    if (!id || id === peaksFor) return;
    peaksFor = id;
    peaks = null;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/peaks", {
          params: { path: { id }, query: { level: "overview" } },
        });
        if (peaksFor === id) {
          peaks = r.data?.peaks.data ?? null;
          bits = r.data?.peaks.bits ?? 8;
        }
      } catch {
        /* the bar works without a waveform */
      }
    })();
  });
</script>

<div class="bar" data-testid="player-bar">
  <div class="buttons">
    <button type="button" aria-label="Previous" onclick={() => player.prev()} disabled={!player.state.trackId}>⏮</button>
    <button
      type="button"
      aria-label={player.state.playing ? "Pause" : "Play"}
      data-testid="play-toggle"
      onclick={() => player.toggle()}
      disabled={!player.state.trackId}>{player.state.playing ? "⏸" : "▶"}</button
    >
    <button type="button" aria-label="Next" onclick={() => player.next()} disabled={player.state.index + 1 >= player.state.queue.length}>⏭</button>
  </div>
  <div class="mid">
    <div class="title">
      {#if player.state.trackId}
        <a href="/tracks/{player.state.trackId}">{player.title}</a>
      {:else}
        <span class="muted">{player.title}</span>
      {/if}
      {#if player.state.error}<span role="alert" class="err">{player.state.error}</span>{/if}
    </div>
    <Waveform
      {peaks}
      {bits}
      duration={player.state.duration}
      current={player.state.current}
      height={32}
      label="Seek"
      onseek={(s) => player.seek(s)}
    />
  </div>
  <span class="time" data-testid="player-time">{fmt(player.state.current)} / {fmt(player.state.duration)}</span>
  {#if trackId && j}
    <div class="judge" data-testid="player-judge">
      <StarRating value={j.stars} onchange={(n) => judge.rate(trackId, n)} label="Rating" />
      <StatusSelect value={j.status} onchange={(st) => judge.setStatus(trackId, st)} />
      <button type="button" aria-expanded={commenting} data-testid="player-comment" onclick={() => (commenting = !commenting)}>Comment</button>
    </div>
  {/if}
  {#if commenting && trackId}
    <div class="pop" data-testid="player-composer">
      <CommentComposer {trackId} current={player.state.current} playing={player.state.playing} oncreated={() => (commenting = false)} oncancel={() => (commenting = false)} />
    </div>
  {/if}
</div>

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1rem;
    align-items: center;
    flex: 1;
    min-width: 0;
  }
  .buttons {
    display: flex;
    gap: 0.25rem;
  }
  .buttons button {
    min-width: 2.5rem;
    min-height: 2.5rem;
  }
  .mid {
    flex: 1 1 8rem;
    min-width: 0;
  }
  .title {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .judge {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .bar {
    position: relative;
  }
  .pop {
    position: absolute;
    right: 0;
    bottom: calc(100% + 0.5rem);
    width: min(24rem, 100%);
    background: #1d1814;
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.6rem;
    z-index: 5;
  }
  .err {
    margin-left: 0.5rem;
    color: #ff9a7a;
  }
  .time {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
</style>

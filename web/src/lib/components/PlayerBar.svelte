<script lang="ts">
  import PrevIcon from "./icons/PrevIcon.svelte";
  import NextIcon from "./icons/NextIcon.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";
  import { api } from "../../api/client";
  import type { Player } from "../player.svelte";
  import { fmt } from "../time";
  import { judge } from "../judge.svelte";
  import { sheet } from "../nowPlaying.svelte";
  import QueueIcon from "./icons/QueueIcon.svelte";
  import CoverArt from "./CoverArt.svelte";
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
  const restoredLabel = $derived(
    `Resume ${player.title}${player.state.restored !== null && player.state.restored >= 1 ? ` at ${fmt(player.state.restored)}` : ""}`,
  );
  const j = $derived(trackId ? judge.get(trackId) : undefined);

  // Seed the judging state for the current track unless a page already did.
  $effect(() => {
    const id = trackId;
    commenting = false;
    // Offline there is no API to ask: the bar works without judging controls and a waveform.
    if (!id || judge.get(id) || navigator.onLine === false) return;
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
    if (navigator.onLine === false) {
      peaks = null;
      return;
    }
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
    <button type="button" aria-label="Previous" onclick={() => player.prev()} disabled={!player.state.trackId}><PrevIcon size={20} /></button>
    <button
      type="button"
      aria-label={player.state.playing ? "Pause" : "Play"}
      data-testid="play-toggle"
      onclick={() => player.toggle()}
      disabled={!player.state.trackId}>{#if player.state.playing}<PauseIcon size={20} />{:else}<PlayIcon size={20} />{/if}</button
    >
    <button type="button" aria-label="Next" onclick={() => player.next()} disabled={!player.hasNext}><NextIcon size={20} /></button>
  </div>
  {#if trackId}<CoverArt {trackId} title={player.title} size={40} />{/if}
  <div class="mid">
    <div class="title">
      {#if player.state.trackId}
        <a href="/tracks/{player.state.trackId}">{player.title}</a>
      {:else}
        <span class="muted">{player.title}</span>
      {/if}
      {#if player.state.noFlac}<span class="mp3chip" data-testid="player-mp3-chip" title="This track has no lossless file; its master is an MP3">MP3</span>{/if}
    </div>
    <div class="status">
        {#if player.state.pendingPlay}
          <button type="button" class="continue" data-testid="player-continue" onclick={() => player.resume()}>Tap to continue</button>
        {:else if player.state.held}
          <button type="button" class="continue" data-testid="player-resume-hold" onclick={() => player.resume()}>Resume {player.title}</button>
        {:else if player.state.restored !== null && !player.state.playing}
          <button type="button" class="continue" data-testid="player-resume" onclick={() => player.resume()}
            >{restoredLabel}</button
          >
        {:else if player.state.error}
          <span role="alert" class="err">{player.state.error}</span>
        {:else if player.state.endOfQueue}
          <span class="muted" data-testid="player-end" role="status">End of queue</span>
        {/if}
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
  <button
    type="button"
    class="open"
    aria-label="Now playing and queue"
    aria-expanded={sheet.open}
    data-testid="player-open-sheet"
    disabled={!trackId}
    onclick={(e) => (sheet.open ? sheet.close() : sheet.show(e.currentTarget))}><QueueIcon size={20} /></button
  >
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
    display: inline-flex;
    align-items: center;
    justify-content: center;
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
  .mp3chip {
    font-size: 0.7rem;
    padding: 0 0.35rem;
    margin-left: 0.4rem;
    border: 1px solid #e0a030;
    border-radius: 0.6rem;
    color: #e0a030;
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
  .status:empty {
    display: none;
  }
  .continue {
    min-height: 2.5rem;
    padding: 0 0.75rem;
  }
  .err {
    color: #ff9a7a;
  }
  .open {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 2.5rem;
    min-height: 2.5rem;
  }
  .time {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
</style>

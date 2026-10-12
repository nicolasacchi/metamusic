<script lang="ts">
  import { api } from "../../api/client";
  import type { Player } from "../player.svelte";
  import { fmt } from "../time";
  import Waveform from "./Waveform.svelte";

  let { player }: { player: Player } = $props();

  let peaks = $state<number[] | null>(null);
  let bits = $state(8);
  let peaksFor = "";

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
</div>

<style>
  .bar {
    display: flex;
    gap: 1rem;
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
    flex: 1;
    min-width: 0;
  }
  .title {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
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

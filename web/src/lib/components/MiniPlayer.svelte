<script lang="ts">
  import NextIcon from "./icons/NextIcon.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";
  import type { Player } from "../player.svelte";
  import CoverArt from "./CoverArt.svelte";
  import { fmt } from "../time";
  import { sheet } from "../nowPlaying.svelte";

  interface Props {
    player: Player;
    /** On /duels/:id the bar shows no title or stars (a blind comparison). */
    duel?: boolean;
  }
  let { player, duel = false }: Props = $props();

  const s = $derived(player.state);
  // One string: the clamped (-webkit-box) button would otherwise lay its text nodes out as separate flex items.
  const restoredLabel = $derived(`Resume ${player.title}${s.restored !== null && s.restored >= 1 ? ` at ${fmt(s.restored)}` : ""}`);
  const pct = $derived(s.duration > 0 ? Math.min(100, Math.max(0, (s.current / s.duration) * 100)) : 0);
</script>

<div class="mini" data-testid="player-bar" class:duel>
  {#if duel}
    <div class="body"><span class="state" data-testid="player-duel">Paused for duel</span></div>
  {:else}
    {#if s.trackId}<CoverArt trackId={s.trackId} title="" size={48} />{/if}
    <!-- The body opens the now-playing sheet; the href keeps /queue reachable (middle click, long press, no JS). -->
    <a
      class="body"
      href="/queue"
      aria-label="Open now playing: {player.title}"
      data-testid="player-open-sheet"
      onclick={(e) => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        sheet.show(e.currentTarget);
      }}
    >
      <span class="title">{player.title}{#if s.noFlac}<span class="mp3chip" data-testid="player-mp3-chip" title="This track has no lossless file; its master is an MP3">MP3</span>{/if}</span>
      {#if s.error}
        <span role="alert" class="sub err">{s.error}</span>
      {:else if s.endOfQueue}
        <span class="sub muted" data-testid="player-end" role="status">End of queue</span>
      {:else if !s.pendingPlay}
        <span class="sub muted">{player.album || (s.playing ? "Playing" : "Paused")}</span>
      {/if}
    </a>
    {#if s.pendingPlay}
      <button type="button" class="continue" data-testid="player-continue" onclick={() => player.resume()}>Tap to continue</button>
    {:else if s.held}
      <button type="button" class="continue resume" data-testid="player-resume-hold" onclick={() => player.resume()}>{`Resume ${player.title}`}</button>
    {:else if s.restored !== null && !s.playing}
      <button type="button" class="continue resume" data-testid="player-resume" onclick={() => player.resume()}
        >{restoredLabel}</button
      >
    {/if}
    <button
      type="button"
      class="ico"
      aria-label={s.playing ? "Pause" : "Play"}
      data-testid="play-toggle" data-transport-toggle
      onclick={() => player.toggle()}
      disabled={!s.trackId}>{#if s.playing}<PauseIcon size={24} />{:else}<PlayIcon size={24} />{/if}</button
    >
    <button type="button" class="ico" aria-label="Next" onclick={() => player.next()} disabled={!player.hasNext}><NextIcon size={24} /></button>
    <div class="progress" aria-hidden="true"><div class="fill" style:width="{pct}%"></div></div>
  {/if}
</div>

<style>
  .mini {
    position: relative;
    box-sizing: border-box;
    flex: 1;
    min-width: 0;
    height: var(--mini-h);
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    padding: 0 var(--sp-3);
    overflow: hidden;
  }
  .body {
    display: grid;
    flex: 1;
    min-width: 0;
    color: var(--text);
    text-decoration: none;
    line-height: var(--lh-tight);
  }
  .title,
  .sub {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub {
    font-size: var(--fs-sm);
  }
  .state {
    color: var(--text-2);
  }
  .err {
    color: var(--bad);
  }
  .ico {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: 48px;
    height: 48px;
    min-height: 48px;
    padding: 0;
    border: none;
    border-radius: var(--r-pill);
  }
  .continue {
    flex: none;
    min-height: var(--hit);
    padding: 0 var(--sp-3);
  }
  .resume {
    flex: 0 1 40%;
    min-width: 0;
    padding: var(--sp-1) var(--sp-2);
    font-size: var(--fs-sm);
    line-height: var(--lh-tight);
    max-height: calc(var(--mini-h) - var(--sp-2));
    overflow: hidden;
    text-align: left;
  }
  .mp3chip {
    font-size: var(--fs-xs);
    padding: 0 var(--sp-1);
    margin-left: var(--sp-2);
    border: 1px solid var(--warn);
    border-radius: var(--r-pill);
    color: var(--warn);
  }
  .progress {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 2px;
    background: var(--line);
  }
  .fill {
    height: 100%;
    background: var(--accent);
  }
</style>

<script lang="ts">
  import type { components } from "../../../api/schema";
  import { judge, type TrackStatus } from "../../judge.svelte";
  import { player } from "../../player.svelte";
  import { STATUSES } from "../../libraryQuery";
  import { ageShort } from "../../inbox";
  import { fmt } from "../../time";
  import CoverArt from "../CoverArt.svelte";
  import MoreIcon from "../icons/MoreIcon.svelte";
  import StarRating from "../StarRating.svelte";

  type Track = components["schemas"]["TrackSummary"];
  interface Props {
    track: Track;
    unheard: boolean;
    playing?: boolean;
    now: Date;
    onplay: (id: string) => void;
  }
  let { track, unheard, playing = false, now, onplay }: Props = $props();

  let open = $state(false);
  let root: HTMLElement | undefined;

  // The queue actions belong to the player lane: offered only when the store has them.
  const p = player as unknown as { playNext?: (id: string) => void; enqueue?: (id: string) => void };
  const canPlayNext = typeof p.playNext === "function";
  const canEnqueue = typeof p.enqueue === "function";

  const stars = $derived(judge.get(track.id)?.stars ?? track.stars);
  const status = $derived(judge.get(track.id)?.status ?? track.status);

  function ensureSeed() {
    if (judge.get(track.id)) return;
    judge.seed(track.id, { stars: track.stars, verdict: null, critic: null, status: track.status, tags: [] });
  }
  function toggle() {
    open = !open;
    if (open) ensureSeed();
  }
  function close() {
    open = false;
  }
  function onwindow(e: Event) {
    if (open && root && !root.contains(e.target as Node)) close();
  }
  function onkey(e: KeyboardEvent) {
    if (e.key === "Escape" && open) {
      close();
      (root?.querySelector("button.more") as HTMLElement | null)?.focus();
    }
  }
</script>

<svelte:window onclick={onwindow} onkeydown={onkey} />

<li class="row" class:playing data-testid="inbox-row" data-track-id={track.id} data-unheard={unheard} bind:this={root}>
  <button type="button" class="main" onclick={() => onplay(track.id)} aria-label="Play {track.title}" data-testid="inbox-play-row">
    <CoverArt trackId={track.id} title="" size={48} />
    <span class="text">
      <span class="title">
        {#if unheard}<span class="dot" data-testid="unheard-dot" aria-label="Not heard yet">•</span>{/if}{track.title}
      </span>
      <span class="meta muted">{fmt(track.duration_s)} · {ageShort(track.recorded_at, now)}{#if track.is_pick}&nbsp;· Pick{/if}{#if track.truncated}&nbsp;· truncated{/if}</span>
    </span>
  </button>
  <button type="button" class="more" aria-label="More for {track.title}" aria-expanded={open} aria-haspopup="menu" onclick={toggle} data-testid="inbox-more"><MoreIcon size={20} /></button>
  {#if open}
    <div class="menu" role="menu" data-testid="inbox-menu">
      {#if canPlayNext}<button type="button" role="menuitem" onclick={() => { p.playNext?.(track.id); close(); }}>Play next</button>{/if}
      {#if canEnqueue}<button type="button" role="menuitem" onclick={() => { p.enqueue?.(track.id); close(); }}>Add to queue</button>{/if}
      <div class="rate"><StarRating value={stars} label="Rate {track.title}" onchange={(n) => void judge.rate(track.id, n)} /></div>
      <label class="status">
        Status
        <select value={status} onchange={(e) => void judge.setStatus(track.id, e.currentTarget.value as TrackStatus)} data-testid="inbox-status">
          {#each STATUSES as s (s)}<option value={s} selected={s === status}>{s}</option>{/each}
        </select>
      </label>
      <a role="menuitem" href="/tracks/{track.id}">Open take</a>
      {#if track.song_id}<a role="menuitem" href="/songs/{track.song_id}">Open Song</a>{/if}
    </div>
  {/if}
</li>

<style>
  .row {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--sp-1);
    border-bottom: 1px solid var(--line);
    min-height: 60px;
  }
  .row.playing {
    background: var(--accent-soft);
  }
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    min-height: var(--hit);
    padding: var(--sp-2) 0;
    background: none;
    border: none;
    color: var(--text);
    text-align: left;
    cursor: pointer;
  }
  .text {
    display: grid;
    min-width: 0;
  }
  .title,
  .meta {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .meta {
    font-size: var(--fs-sm);
  }
  .dot {
    color: var(--accent);
    margin-right: var(--sp-1);
    font-weight: var(--fw-bold);
  }
  .more {
    min-width: var(--hit);
    min-height: var(--hit);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    background: none;
    border: none;
    color: var(--text-2);
    cursor: pointer;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% - var(--sp-2));
    z-index: var(--z-sheet);
    display: grid;
    gap: var(--sp-1);
    min-width: 12rem;
    padding: var(--sp-2);
    background: var(--surface-2);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
  }
  .menu button,
  .menu a,
  .status {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
    padding: 0 var(--sp-3);
    color: var(--text);
    text-decoration: none;
  }
  .menu button {
    background: none;
    border: none;
    text-align: left;
    cursor: pointer;
  }
  .rate {
    padding: 0 var(--sp-2);
  }
</style>

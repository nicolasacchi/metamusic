<script lang="ts">
  import type { components } from "../../api/schema";
  import { fmt } from "../time";

  type Item = components["schemas"]["PlaylistItem"];
  interface Props {
    items: readonly Item[];
    busy?: boolean;
    playingId?: string;
    onmove: (index: number, delta: number) => void;
    onremove: (index: number) => void;
    onplay: (id: string) => void;
    onplayall: () => void;
  }
  let { items, busy = false, playingId = "", onmove, onremove, onplay, onplayall }: Props = $props();
</script>

<div class="bar">
  <button type="button" onclick={onplayall} disabled={items.length === 0} data-testid="playlist-play-all">Play all</button>
</div>
{#if items.length === 0}
  <p class="muted">This playlist is empty. Add tracks from a track page.</p>
{:else}
  <ol data-testid="playlist-items">
    {#each items as it, i (i + ":" + it.track.id)}
      <li data-track-id={it.track.id} class:playing={it.track.id === playingId}>
        <button type="button" class="title" onclick={() => onplay(it.track.id)}>{it.track.title}</button>
        <a href="/tracks/{it.track.id}" class="muted">open</a>
        <span class="muted">{fmt(it.track.duration_s)}</span>
        <span class="ctl">
          <button type="button" aria-label="Move {it.track.title} up" onclick={() => onmove(i, -1)} disabled={busy || i === 0}>↑</button>
          <button type="button" aria-label="Move {it.track.title} down" onclick={() => onmove(i, 1)} disabled={busy || i === items.length - 1}>↓</button>
          <button type="button" aria-label="Remove {it.track.title}" onclick={() => onremove(i)} disabled={busy}>Remove</button>
        </span>
      </li>
    {/each}
  </ol>
{/if}

<style>
  ol {
    padding-left: 1.5rem;
  }
  li {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    padding: 0.25rem 0;
  }
  li.playing .title {
    font-weight: 700;
  }
  .title {
    background: none;
    border: 0;
    color: inherit;
    text-align: left;
    cursor: pointer;
    padding: 0.4rem 0;
  }
  .ctl {
    margin-left: auto;
    display: flex;
    gap: 0.3rem;
  }
</style>

<script lang="ts">
  import PlayIcon from "./icons/PlayIcon.svelte";
  import MoreIcon from "./icons/MoreIcon.svelte";
  import CoverArt from "./CoverArt.svelte";
  import TrackBadges from "./TrackBadges.svelte";
  import type { components } from "../../api/schema";
  import { fmt } from "../time";
  import { ageLabel } from "../approvals";
  import { judge } from "../judge.svelte";
  import { templateSelectable } from "../genIntent";

  type Track = components["schemas"]["TrackSummary"];
  interface Props {
    tracks: readonly Track[];
    playingId?: string;
    onplay: (id: string) => void;
    onplaynext?: (id: string) => void;
    /** Selection mode: a 44 px checkbox label per row; reference and lossy rows are disabled. */
    selected?: ReadonlySet<string> | null;
    onselect?: (id: string, on: boolean) => void;
  }
  let { tracks, playingId = "", onplay, onplaynext, selected = null, onselect }: Props = $props();

  // A rating changed in this session shows at once, before the next fetch.
  function stars(t: Track): number | null {
    return judge.get(t.id)?.stars ?? t.stars;
  }
  function meta(t: Track): string {
    const first = t.style_terms[0];
    return [first, fmt(t.duration_s), ageLabel(t.recorded_at, Date.now())].filter(Boolean).join(" · ");
  }
  let menuFor = $state("");
  function pick(fn: (() => void) | undefined) {
    menuFor = "";
    fn?.();
  }
</script>

<ul class="list" data-testid="track-list">
  {#each tracks as t (t.id)}
    <li class="row" data-testid="track-row" data-track-id={t.id} class:playing={t.id === playingId}>
      {#if selected}
        <label class="sel" title={templateSelectable(t) ? "" : "recipe unknown"}>
          <input
            type="checkbox"
            data-testid="row-select"
            aria-label="Select {t.title}"
            checked={selected.has(t.id)}
            disabled={!templateSelectable(t)}
            onchange={(e) => onselect?.(t.id, e.currentTarget.checked)}
          />
        </label>
      {/if}
      <button type="button" class="cover" aria-label="Play {t.title}" onclick={() => onplay(t.id)}>
        <CoverArt trackId={t.id} title="" size={48} />
        <span class="glyph" aria-hidden="true"><PlayIcon size={20} /></span>
      </button>
      <div class="text">
        <a href="/tracks/{t.id}" class="title" data-testid="track-link">{t.title}</a>
        <span class="meta">
          <TrackBadges recipeKnown={t.recipe_known} lossy={t.lossy} isPick={t.is_pick} />{meta(t)}{#if t.truncated} · truncated{/if}
        </span>
      </div>
      <span class="stars" data-testid="row-stars" role="img" aria-label={stars(t) === null ? "Unrated" : `${stars(t)} stars`}>
        {#if stars(t) === null}<span class="muted">—</span>{:else}{"★".repeat(stars(t) ?? 0)}{/if}
      </span>
      <span data-testid="row-status" class="sr">{judge.get(t.id)?.status ?? t.status}</span>
      <div class="menuwrap">
        <button type="button" class="more" aria-label="More for {t.title}" aria-haspopup="menu" aria-expanded={menuFor === t.id} onclick={() => (menuFor = menuFor === t.id ? "" : t.id)}><MoreIcon size={20} /></button>
        {#if menuFor === t.id}
          <div class="menu" role="menu">
            <button type="button" role="menuitem" onclick={() => pick(() => onplay(t.id))}>Play</button>
            {#if onplaynext}<button type="button" role="menuitem" aria-label="Queue {t.title} next" onclick={() => pick(() => onplaynext(t.id))}>Play next</button>{/if}
            <a href="/tracks/{t.id}" role="menuitem">Open track</a>
          </div>
        {/if}
      </div>
    </li>
  {:else}
    <li class="muted empty">No tracks match.</li>
  {/each}
</ul>

<style>
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .row {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    height: 64px;
    border-bottom: 1px solid var(--line);
  }
  .row.playing {
    background: var(--accent-soft);
  }
  .sel {
    display: flex;
    align-items: center;
    justify-content: center;
    width: var(--hit);
    height: var(--hit);
    flex: none;
  }
  .sel input {
    width: 1.4rem;
    height: 1.4rem;
  }
  .cover {
    position: relative;
    flex: none;
    width: 48px;
    height: 48px;
    padding: 0;
    border: none;
    border-radius: var(--r-sm);
    overflow: hidden;
  }
  .glyph {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.35);
    color: #fff;
  }
  .text {
    position: relative;
    flex: 1;
    min-width: 0;
    height: 64px;
  }
  /* the whole text column is the link target (64 px); the meta line sits on top of it and lets taps through */
  .title {
    display: block;
    box-sizing: border-box;
    height: 64px;
    padding-top: 10px;
    font-size: var(--fs-md);
    font-weight: 600;
    color: var(--text);
    text-decoration: none;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .meta {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 10px;
    pointer-events: none;
    font-size: var(--fs-xs);
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .stars {
    flex: none;
    color: var(--accent);
    font-size: var(--fs-sm);
    letter-spacing: -0.05em;
  }
  .menuwrap {
    flex: none;
  }
  .more {
    width: var(--hit);
    height: var(--hit);
    padding: 0;
    border: none;
    display: grid;
    place-items: center;
  }
  .menu {
    position: absolute;
    right: 0;
    top: 52px;
    z-index: 4;
    display: grid;
    min-width: 11rem;
    background: var(--bg-raised);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .menu > * {
    display: flex;
    align-items: center;
    box-sizing: border-box;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: none;
    border-radius: 0;
    color: var(--text);
    text-decoration: none;
    font-size: var(--fs-md);
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  .empty {
    padding: var(--sp-4) 0;
  }
</style>

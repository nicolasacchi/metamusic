<script lang="ts">
  // One take as a card: it IS the player while it is on screen (the footer mini-player steps aside), so it drives the
  // global player store and inherits its rules (audio focus lease, queue, Media Session). Blind mode shows "Take A" and
  // nothing else: no title, Song, angle, cover or waveform in the DOM.
  import { player } from "../player.svelte";
  import { fmt } from "../time";
  import { cardView, chorusStart, type JudgeTake, type TimedSection } from "../judgeQueue";
  import type { TrackStatus } from "../judge.svelte";
  import CoverArt from "./CoverArt.svelte";
  import StarRating from "./StarRating.svelte";
  import StatusChips from "./StatusChips.svelte";
  import Waveform from "./Waveform.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";

  interface Props {
    take: JudgeTake;
    index: number;
    blind: boolean;
    sections: readonly TimedSection[];
    peaks: readonly number[] | null;
    bits: number;
    stars: number | null;
    status: string;
    /** The Song has a Pick that is not this take. */
    canDuel: boolean;
    /** Start this take in the player with the list as its queue. */
    play: (startAt?: number) => void;
    onrate: (stars: number | null) => void;
    onstatus: (s: TrackStatus) => void;
    onskip: () => void;
    onduel: () => void;
  }
  let { take, index, blind, sections, peaks, bits, stars, status, canDuel, play, onrate, onstatus, onskip, onduel }: Props = $props();

  const view = $derived(cardView(take, index, blind));
  const s = $derived(player.state);
  const isCur = $derived(s.trackId === take.id);
  const playing = $derived(isCur && s.playing);
  const duration = $derived(isCur && s.duration > 0 ? s.duration : take.duration_s);
  const current = $derived(isCur ? s.current : 0);
  const chorus = $derived(chorusStart(sections));

  function toggle() {
    if (isCur) player.toggle();
    else play();
  }
  function jump(by: number) {
    if (isCur) player.seek(Math.max(0, current + by));
    else play(Math.max(0, by));
  }
  function toChorus() {
    if (chorus === null) return;
    if (isCur) {
      player.seek(chorus);
      if (player.el?.paused) player.resume();
    } else play(chorus);
  }
  function seek(t: number) {
    if (isCur) player.seek(t);
    else play(t);
  }
</script>

<article class="card" data-testid="judge-card" data-track-id={take.id} data-blind={blind}>
  <div class="cover">
    {#if view.cover}
      <CoverArt trackId={take.id} title="" size={240} />
    {:else}
      <div class="blindcover" data-testid="judge-blind-cover" aria-hidden="true">?</div>
    {/if}
  </div>
  <h2 class="title" data-testid="judge-title">{view.title}</h2>
  <p class="sub muted" data-testid="judge-sub">
    {#if view.songLine}<span data-testid="judge-song-line">{view.songLine}</span> · {/if}{fmt(take.duration_s)}{#if take.is_pick && !blind} · Pick{/if}
  </p>

  {#if view.waveform}
    <Waveform {peaks} {bits} {duration} {current} height={56} label="Seek" onseek={seek} />
  {/if}
  <div class="times" aria-hidden="true">
    <span data-testid="judge-time">{fmt(current)}</span>
    <span class="muted">{duration > 0 ? fmt(duration) : ""}</span>
  </div>

  <div class="transport">
    {#if chorus !== null}
      <button type="button" class="chorus" data-testid="judge-chorus" onclick={toChorus}>Jump to chorus {fmt(chorus)}</button>
    {/if}
    <button type="button" class="jump" aria-label="Back 10 seconds" data-testid="judge-back10" onclick={() => jump(-10)}>−10 s</button>
    <button type="button" class="big" aria-label={playing ? "Pause" : "Play"} data-testid="judge-play" onclick={toggle}>
      {#if playing}<PauseIcon size={36} />{:else}<PlayIcon size={36} />{/if}
    </button>
    <button type="button" class="jump" aria-label="Forward 10 seconds" data-testid="judge-fwd10" onclick={() => jump(10)}>+10 s</button>
  </div>

  <div class="stars" data-testid="judge-stars">
    <StarRating value={stars} label="Your rating" size={52} onchange={onrate} />
  </div>
  <div class="chips"><StatusChips value={status} onchange={onstatus} /></div>

  <div class="acts">
    {#if canDuel}<button type="button" data-testid="judge-duel" onclick={onduel}>Duel vs Pick</button>{/if}
    {#if view.openTake}<a class="btn" href="/tracks/{take.id}" data-testid="judge-open">Open take</a>{/if}
    <button type="button" data-testid="judge-skip" onclick={onskip}>Skip ›</button>
  </div>
</article>

<style>
  .card {
    display: grid;
    gap: var(--sp-3);
    justify-items: stretch;
    max-width: 32rem;
    margin: 0 auto;
  }
  .cover {
    display: grid;
    place-items: center;
  }
  .cover :global(.cover) {
    width: min(70vw, 240px);
    height: min(70vw, 240px);
  }
  .blindcover {
    display: grid;
    place-items: center;
    width: min(70vw, 240px);
    height: min(70vw, 240px);
    border-radius: var(--r-lg);
    background: var(--surface-2);
    border: 1px dashed var(--line-strong);
    font-size: 4rem;
    color: var(--text-2);
  }
  .title {
    margin: 0;
    font-size: var(--fs-lg);
    text-align: center;
    overflow-wrap: anywhere;
  }
  .sub {
    margin: 0;
    text-align: center;
    font-size: var(--fs-sm);
  }
  .times {
    display: flex;
    justify-content: space-between;
    font-variant-numeric: tabular-nums;
    font-family: ui-monospace, monospace;
    font-size: var(--fs-sm);
  }
  .transport {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    gap: var(--sp-3);
  }
  .chorus {
    flex: 1 1 100%;
    min-height: var(--hit);
  }
  .jump {
    min-width: var(--hit-lg);
    min-height: var(--hit-lg);
    padding: 0 var(--sp-3);
    border-radius: var(--r-pill);
  }
  .big {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--hit-xl);
    height: var(--hit-xl);
    padding: 0;
    border-radius: var(--r-pill);
  }
  .stars,
  .chips {
    display: flex;
    justify-content: center;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    justify-content: space-between;
  }
  .acts button,
  .acts .btn {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    color: var(--text);
    text-decoration: none;
    background: transparent;
    cursor: pointer;
  }
</style>

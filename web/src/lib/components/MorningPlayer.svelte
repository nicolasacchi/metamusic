<script lang="ts">
  // The Morning Crate's listening controls (M6N-W2, C384.2/3): play a side, the one-handed buttons (mark, skip, back) that
  // speak the same grammar as the headset, and the blind stars. The sound comes from the app's one audio element: nothing plays here.
  import { player } from "../player.svelte";
  import { fmt } from "../time";
  import { chapterAt, loadPreset, parseMorningItem, savePreset, type MorningCrate } from "../morningCrate";
  import type { HeadsetPreset } from "../headset";
  import StarRating from "./StarRating.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";

  interface Props {
    crate: MorningCrate;
    /** Stars as the Owner has just set them (before the server has them), chapter number -> stars. */
    stars: Record<number, number | null>;
    onrate: (chapter: number, stars: number | null) => void;
  }
  let { crate, stars, onrate }: Props = $props();

  let preset = $state<HeadsetPreset>(loadPreset());

  const item = $derived(parseMorningItem(player.state.trackId));
  const ours = $derived(player.state.mode === "crate" && item?.crate === crate.id);
  const here = $derived(ours && item ? chapterAt(crate, item.side, Math.round(player.state.current * 1000)) : null);
  const shown = (n: number, server: number | null) => (n in stars ? stars[n]! : server);
  const unrated = $derived(crate.chapters.filter((c) => shown(c.n, c.stars) === null).length);

  function setPreset(p: HeadsetPreset) {
    preset = p;
    savePreset(p);
    player.crate?.setPreset(p);
  }
</script>

<div class="mp" data-testid="morning-player">
  <div class="sides">
    {#each crate.sides as s (s.side)}
      {#if s.removed || !s.url}
        <span class="side gone" data-testid="morning-side-gone">{s.label}: removed</span>
      {:else}
        <button type="button" class="side" data-testid="morning-side-{s.side}" onclick={() => player.playCrate(crate, s.side, 0)}>
          <PlayIcon size={20} /> {s.label} <span class="muted">{Math.round(s.duration_ms / 60_000)} min</span>
        </button>
      {/if}
    {/each}
  </div>

  {#if ours}
    <p class="now" role="status" aria-live="polite" data-testid="morning-now">{player.title}<span class="muted"> · {player.album} · {fmt(player.state.current)}</span></p>
    <div class="pad" data-testid="morning-pad">
      <button type="button" class="big" aria-label="Back to the start of this chapter" onclick={() => player.prev()} data-testid="morning-back">Back</button>
      <button type="button" class="big go" aria-label={player.state.playing ? "Pause" : "Play"} onclick={() => player.toggle()} data-testid="morning-toggle">
        {#if player.state.playing}<PauseIcon size={28} />{:else}<PlayIcon size={28} />{/if}
      </button>
      <button type="button" class="big" aria-label="Skip this chapter" onclick={() => player.crate?.skip()} data-testid="morning-skip">Skip</button>
      <button type="button" class="big mark" aria-label="Mark this moment; press twice to keep it" onclick={() => player.crate?.mark()} data-testid="morning-mark">Mark</button>
    </div>
    <p class="muted hint">Mark once to note a moment; twice within three seconds to keep it. The headset does the same.</p>
  {/if}

  <fieldset class="preset">
    <legend>Headset</legend>
    <label><input type="radio" name="preset" checked={preset === "prev_marks"} onchange={() => setPreset("prev_marks")} data-testid="morning-preset-prev" /> Previous marks, next skips</label>
    <label><input type="radio" name="preset" checked={preset === "next_marks"} onchange={() => setPreset("next_marks")} data-testid="morning-preset-next" /> Next marks, previous skips</label>
  </fieldset>

  <h2>Rate each take <span class="muted" data-testid="morning-unrated">{unrated === 0 ? "all rated" : `${unrated} left`}</span></h2>
  <ol class="chapters">
    {#each crate.chapters as c (c.n)}
      <li class:cur={here?.n === c.n} data-testid="morning-chapter" data-n={c.n} aria-current={here?.n === c.n ? "true" : undefined}>
        <span class="n">Take {c.n}</span>
        <StarRating value={shown(c.n, c.stars)} onchange={(v) => onrate(c.n, v)} label="Rate take {c.n}" />
      </li>
    {/each}
  </ol>
</div>

<style>
  .mp {
    display: grid;
    gap: var(--sp-3);
  }
  .sides {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
  }
  .side {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--accent);
    border-radius: var(--r-pill);
    background: transparent;
    color: var(--accent);
  }
  .gone {
    color: var(--text-2);
    border-color: var(--line);
  }
  .now {
    margin: 0;
    font-size: var(--fs-lg, 1.2rem);
  }
  .pad {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: var(--sp-3);
  }
  .big {
    min-height: 4.5rem;
    font-size: 1.2rem;
    border-radius: var(--r-md);
    border: 1px solid var(--line);
    background: var(--surface);
    color: inherit;
  }
  .big.mark {
    border-color: var(--accent);
    color: var(--accent);
  }
  .hint {
    margin: 0;
  }
  .preset {
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    display: grid;
    gap: var(--sp-1);
  }
  .preset label {
    display: flex;
    gap: var(--sp-2);
    align-items: center;
    min-height: var(--hit);
  }
  .chapters {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .chapters li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--sp-2) var(--sp-3);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
  }
  .chapters li.cur {
    border-color: var(--accent);
  }
  .n {
    font-weight: var(--fw-medium);
  }
</style>

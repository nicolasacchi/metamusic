<script lang="ts">
  import PlayIcon from "./icons/PlayIcon.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { fmt } from "../time";
  import { player } from "../player.svelte";
  import { judge } from "../judge.svelte";
  import { pinAction } from "../songView";
  import PickBadge from "./PickBadge.svelte";
  import StarRating from "./StarRating.svelte";
  import StatusSelect from "./StatusSelect.svelte";
  import TempoBadge from "./TempoBadge.svelte";
  import TrackBadges from "./TrackBadges.svelte";
  import Waveform from "./Waveform.svelte";

  type Take = components["schemas"]["TakeCard"];
  interface Props {
    take: Take;
    /** Queue for the player: every take of the Song in display order. */
    queue: string[];
    pickSource?: string | null;
    pickEvidence?: unknown;
    /** The operator may pin and start duels (human principal). */
    canPin?: boolean;
    pinIneligible?: boolean;
    selected?: boolean;
    onpin?: (takeId: string | null) => void;
    onselect?: (takeId: string) => void;
    onchanged?: () => void;
  }
  let { take, queue, pickSource = null, pickEvidence, canPin = false, pinIneligible = false, selected = false, onpin, onselect, onchanged }: Props = $props();

  let peaks = $state<number[] | null>(null);
  let bits = $state(8);
  let host: HTMLElement | undefined = $state();

  // Lazy overview peaks: fetched once the card scrolls into view (immediately where IntersectionObserver is missing).
  $effect(() => {
    const el = host;
    const id = take.id;
    if (!el) return;
    let done = false;
    const load = async () => {
      if (done) return;
      done = true;
      try {
        const r = await api.GET("/api/tracks/{id}/peaks", { params: { path: { id }, query: { level: "overview" } } });
        if (r.data?.peaks.data) {
          peaks = r.data.peaks.data;
          bits = r.data.peaks.bits;
        }
      } catch {
        /* the card works without a waveform */
      }
    };
    if (typeof IntersectionObserver === "undefined") {
      void load();
      return;
    }
    const io = new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) {
        io.disconnect();
        void load();
      }
    });
    io.observe(el);
    return () => io.disconnect();
  });

  const j = $derived(judge.get(take.id));
  const stars = $derived(j ? j.stars : take.stars);
  const status = $derived(j ? j.status : take.status);
  const action = $derived(pinAction({ pinned: take.pinned, status }));
  const isCurrent = $derived(player.state.trackId === take.id);
  const position = $derived(isCurrent ? player.state.current : 0);
  const duration = $derived(isCurrent && player.state.duration > 0 ? player.state.duration : take.duration_s);

  function play(at?: number) {
    if (isCurrent && at === undefined) return player.toggle();
    if (isCurrent) {
      player.seek(at ?? 0);
      if (!player.state.playing) player.toggle();
      return;
    }
    player.play(take.id, queue, { title: take.title, ...(at !== undefined ? { startAt: at } : {}) });
  }
  async function rate(n: number | null) {
    if (await judge.rate(take.id, n)) onchanged?.();
  }
  async function setStatus(s: Parameters<typeof judge.setStatus>[1]) {
    if (await judge.setStatus(take.id, s)) onchanged?.();
  }
</script>

<article class="take" class:pick={take.is_pick} class:sel={selected} data-testid="take-card" data-take-id={take.id} bind:this={host}>
  <header>
    <h3><a href="/tracks/{encodeURIComponent(take.id)}">{take.title}</a></h3>
    {#if take.is_pick}<PickBadge source={pickSource} evidence={pickEvidence} />{/if}
    {#if take.pinned && (pinIneligible || status === "discarded")}<span class="muted" data-testid="pin-ineligible">pinned, not eligible</span>{/if}
    <TrackBadges recipeKnown={take.recipe_known} lossy={take.lossy} />
  </header>
  <p class="meta muted">
    {fmt(take.duration_s)} · {take.recorded_at.slice(0, 10)} · gen {take.generation}
    {#if take.parent_track_id} · <a href="/tracks/{encodeURIComponent(take.parent_track_id)}">parent</a>{/if}
    {#if take.cost_display_usd} · {take.cost_display_usd}{/if}
  </p>
  <div class="wave">
    <button type="button" class="play" onclick={() => play()} aria-label={isCurrent && player.state.playing ? "Pause" : "Play"} data-testid="take-play">{#if isCurrent && player.state.playing}<PauseIcon size={20} />{:else}<PlayIcon size={20} />{/if}</button>
    <div class="w"><Waveform {peaks} {duration} current={position} onseek={(s) => play(s)} {bits} height={48} label="Waveform of {take.title}" /></div>
  </div>
  <div class="row">
    <StarRating value={stars} onchange={rate} label="Your rating" />
    <StatusSelect value={status} onchange={setStatus} />
    {#if take.critic_stars !== null}<span class="muted" data-testid="critic">critic ★{take.critic_stars}</span>{/if}
    <TempoBadge tempo={take.tempo} />
  </div>
  {#if take.ladder}
    <p class="muted ladder" data-testid="take-ladder">u {take.ladder.u.toFixed(2)} ± {take.ladder.sd.toFixed(2)} · {take.ladder.n_duels} {take.ladder.n_duels === 1 ? "duel" : "duels"}</p>
  {/if}
  {#if take.flags.length > 0}
    <p class="flags">{#each take.flags as f (f)}<span class="flag" data-testid="ears-flag">{f.replaceAll("_", " ")}</span>{/each}</p>
  {/if}
  {#if canPin}
    <div class="row actions">
      {#if action === "pin"}
        <button type="button" data-testid="pin" onclick={() => onpin?.(take.id)}>Pin as Pick</button>
      {:else if action === "unpin"}
        <button type="button" data-testid="unpin" onclick={() => onpin?.(null)}>Unpin</button>
      {/if}
      <button type="button" data-testid="duel-select" aria-pressed={selected} onclick={() => onselect?.(take.id)}>{selected ? "Selected for duel" : "Duel…"}</button>
    </div>
  {/if}
</article>

<style>
  .take {
    border: 1px solid #5a4d43;
    border-radius: 0.5rem;
    padding: 0.6rem 0.8rem;
  }
  .take.pick {
    border-color: #f2a65a;
  }
  .take.sel {
    outline: 2px solid #f2a65a;
  }
  header {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
  }
  h3 {
    margin: 0;
    font-size: 1rem;
  }
  .meta,
  .ladder {
    margin: 0.2rem 0;
    font-size: 0.85rem;
  }
  .wave {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }
  .w {
    flex: 1;
    min-width: 0;
  }
  .play {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: center;
    margin: 0.3rem 0;
  }
  .flag {
    display: inline-block;
    margin-right: 0.3rem;
    padding: 0 0.4rem;
    border: 1px solid #d9822b;
    border-radius: 0.6rem;
    font-size: 0.8rem;
  }
  @media (pointer: coarse) {
    .actions button {
      min-height: 2.75rem;
    }
  }
</style>

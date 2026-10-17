<script lang="ts">
  import { onDestroy } from "svelte";
  import { fetchClip, ClipGone, ClipLinkExpired } from "../../api/media";
  import { createAB, playGesture, type AB, type Slot } from "../ab";
  import { getAudioContext } from "../audioContext";
  import { player } from "../player.svelte";
  import type { Lease } from "../audioFocus";

  interface Props {
    aUrl: string;
    bUrl: string;
    /** Called once when a clip fetch answered 403/404: returns fresh URLs (get_duel) or null. */
    remint?: () => Promise<{ aUrl: string; bUrl: string } | null>;
    onfirstplay?: () => void;
  }
  let { aUrl, bUrl, remint, onfirstplay }: Props = $props();

  let status = $state<"loading" | "ready" | "error">("loading");
  let error = $state("");
  let active = $state<Slot>("a");
  let playing = $state(false);
  let position = $state(0);
  let duration = $state(0);
  let switches = $state(0);
  let ab: AB | null = null;
  let raf = 0;
  let aborter: AbortController | null = null;
  let firstPlayed = false;
  /** Audio focus lease on the main player: held from the first Play until the verdict or leaving the duel. */
  let lease: Lease | null = null;

  async function loadPair(urls: { aUrl: string; bUrl: string }, signal: AbortSignal) {
    const ctx = getAudioContext();
    const [ra, rb] = await Promise.all([fetchClip(urls.aUrl, signal), fetchClip(urls.bUrl, signal)]);
    const [ba, bb] = await Promise.all([ctx.decodeAudioData(ra), ctx.decodeAudioData(rb)]);
    return { ab: createAB(ctx, ba, bb), duration: Math.min(ba.duration, bb.duration) };
  }

  async function load() {
    aborter?.abort();
    const ac = new AbortController();
    aborter = ac;
    ab?.dispose();
    ab = null;
    status = "loading";
    error = "";
    try {
      let urls: { aUrl: string; bUrl: string } | null = { aUrl, bUrl };
      let loaded;
      try {
        loaded = await loadPair(urls, ac.signal);
      } catch (e) {
        if (!(e instanceof ClipLinkExpired || e instanceof ClipGone) || !remint) throw e;
        urls = await remint();
        if (!urls) throw e;
        loaded = await loadPair(urls, ac.signal);
      }
      if (ac.signal.aborted) return;
      ab = loaded.ab;
      duration = loaded.duration;
      status = "ready";
    } catch (e) {
      if (ac.signal.aborted) return;
      status = "error";
      error = e instanceof Error ? e.message : "Could not load the clips";
    }
  }

  $effect(() => {
    void aUrl;
    void bUrl;
    void load();
  });

  function tick() {
    if (ab && playing) {
      position = ab.position();
      raf = requestAnimationFrame(tick);
    }
  }

  /** Play handler: ctx.resume() runs synchronously inside the click (iOS), before any await. */
  export function play() {
    if (!ab) return;
    // Borrow the output instead of pausing the player for good: the verdict (or leaving the route) gives it back.
    lease = player.acquireFocus("duel", {
      handlers: { play: () => play(), pause: () => pause(), playing: () => playing },
      // The shared AudioContext only stops holding the iOS audio session; it is never closed.
      onEnd: () => void getAudioContext().suspend().catch(() => {}),
    });
    if (ab.playing) {
      void ab.resume();
    } else {
      void playGesture(ab, 0);
    }
    playing = true;
    if (!firstPlayed) {
      firstPlayed = true;
      onfirstplay?.();
    }
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(tick);
    lease?.changed();
  }
  export function pause() {
    if (!ab) return;
    void ab.pause();
    playing = false;
    lease?.changed();
  }
  /** Verdict submitted or duel left: stop the A/B and hand the audio back (resume at once inside the gesture). */
  export function releaseFocus(o: { resume?: boolean } = { resume: true }) {
    if (playing) pause();
    lease?.release(o);
    lease = null;
  }
  export function playPause() {
    if (playing) pause();
    else play();
  }
  export function select(s: Slot) {
    ab?.select(s);
    active = ab?.active ?? s;
    switches = ab?.switches ?? switches;
  }
  export function toggle() {
    select(active === "a" ? "b" : "a");
  }
  export function restart() {
    if (!ab) return;
    ab.seek(0);
    position = 0;
  }
  export function stats() {
    return { switches: ab?.switches ?? 0 };
  }

  function onbar(e: MouseEvent) {
    if (!ab || duration <= 0) return;
    const el = e.currentTarget as HTMLElement;
    const r = el.getBoundingClientRect();
    const f = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    ab.seek(f * duration);
    position = f * duration;
  }

  onDestroy(() => {
    lease?.release({ resume: true });
    lease = null;
    aborter?.abort();
    cancelAnimationFrame(raf);
    ab?.dispose();
    ab = null;
  });
</script>

<section class="ab" data-testid="ab-player" aria-label="A/B player">
  {#if status === "loading"}
    <p class="note" role="status">Loading clips…</p>
  {:else if status === "error"}
    <p class="err" role="alert">{error}</p>
    <button type="button" onclick={() => void load()}>Retry</button>
  {:else}
    <div class="row">
      <button type="button" class="play" data-testid="ab-play" onclick={playPause}>{playing ? "Pause" : "Play"}</button>
      <button type="button" class="restart" onclick={restart}>Restart</button>
    </div>
    <button type="button" class="toggle" data-testid="ab-toggle" aria-pressed={active === "b"} aria-label="Switch between A and B" onclick={toggle}>
      {active === "a" ? "A" : "B"}
    </button>
    <div class="row">
      <button type="button" class="slot" class:on={active === "a"} aria-pressed={active === "a"} onclick={() => select("a")}>A</button>
      <button type="button" class="slot" class:on={active === "b"} aria-pressed={active === "b"} onclick={() => select("b")}>B</button>
    </div>
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="bar" onclick={onbar} data-testid="ab-position">
      <div class="fill" style:width="{duration > 0 ? Math.min(100, (position / duration) * 100) : 0}%"></div>
    </div>
    <p class="note">Switches: {switches}</p>
    <p class="keys">Keys: a / b select · space toggle · p play/pause · r restart · 1–4 verdict · Enter submit</p>
  {/if}
</section>

<style>
  .ab {
    display: grid;
    gap: 0.75rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
  }
  button {
    min-height: 48px;
    padding: 0 1rem;
    cursor: pointer;
  }
  .toggle {
    width: 100%;
    min-height: 96px;
    font-size: 2.5rem;
    font-weight: 700;
  }
  .slot {
    flex: 1;
  }
  .slot.on {
    outline: 3px solid #f2a65a;
  }
  .bar {
    height: 12px;
    background: #8884;
    border-radius: 6px;
    cursor: pointer;
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: #f2a65a;
  }
  .note,
  .keys {
    margin: 0;
    opacity: 0.75;
    font-size: 0.85rem;
  }
  .err {
    color: #e5484d;
    margin: 0;
  }
</style>

<script lang="ts">
  // Now-playing sheet (phone: bottom sheet over a scrim; desktop: 360 px right panel). Mounted once in App.svelte.
  // Opening and closing never touch playback. Phone: opening pushes a history entry so Back closes the sheet.
  import { tick } from "svelte";
  import { api } from "../../api/client";
  import { fmt } from "../time";
  import { isTapeItem } from "../recordMedia";
  import { judge } from "../judge.svelte";
  import { player } from "../player.svelte";
  import { dragCloses, sheet } from "../nowPlaying.svelte";
  import { viewport } from "../viewport.svelte";
  import CoverArt from "./CoverArt.svelte";
  import CommentComposer from "./CommentComposer.svelte";
  import QueueList from "./QueueList.svelte";
  import StarRating from "./StarRating.svelte";
  import StatusChips from "./StatusChips.svelte";
  import Waveform from "./Waveform.svelte";
  import DownIcon from "./icons/DownIcon.svelte";
  import NextIcon from "./icons/NextIcon.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";
  import PrevIcon from "./icons/PrevIcon.svelte";
  import QueueIcon from "./icons/QueueIcon.svelte";

  const s = $derived(player.state);
  const trackId = $derived(s.trackId);
  /** A `tape:<id>` current item is not a take: no rating, comments, cover fetch, peaks or take link. */
  const tape = $derived(isTapeItem(trackId));
  const j = $derived(trackId && !tape ? judge.get(trackId) : undefined);
  const phone = $derived(viewport.phone);
  let root = $state<HTMLElement>();
  let closeBtn = $state<HTMLButtonElement>();
  let peaks = $state<number[] | null>(null);
  let bits = $state(8);
  let commenting = $state(false);

  // Seed the judging state of the current track unless a page already did (the phone has no desktop bar to do it).
  $effect(() => {
    const id = trackId;
    commenting = false;
    if (!sheet.open || !id || tape || judge.get(id) || navigator.onLine === false) return;
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
        /* the sheet works without judging controls */
      }
    })();
  });

  let peaksFor = "";
  $effect(() => {
    const id = trackId;
    if (!sheet.open || !id || id === peaksFor) return;
    if (isTapeItem(id)) {
      peaks = null;
      return;
    }
    peaksFor = id;
    peaks = null;
    if (navigator.onLine === false) return;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/peaks", { params: { path: { id }, query: { level: "overview" } } });
        if (peaksFor === id) {
          peaks = r.data?.peaks.data ?? null;
          bits = r.data?.peaks.bits ?? 8;
        }
      } catch {
        /* no waveform */
      }
    })();
  });

  // Focus moves in when the sheet opens; the store returns it to the opener when it closes.
  $effect(() => {
    if (!sheet.open) return;
    void tick().then(() => closeBtn?.focus());
  });

  // Desktop panel: the page makes room for it.
  $effect(() => {
    const on = sheet.open && !phone;
    document.documentElement.style.setProperty("--panel-pad", on ? "var(--panel-w)" : "0px");
    return () => document.documentElement.style.setProperty("--panel-pad", "0px");
  });

  function onkeydown(e: KeyboardEvent) {
    if (!sheet.open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      sheet.close();
      return;
    }
    if (e.key !== "Tab" || !phone || !root) return;
    // modal on a phone: Tab wraps inside the sheet
    const items = [...root.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter((x) => x.offsetParent !== null);
    if (items.length === 0) return;
    const first = items[0]!;
    const last = items[items.length - 1]!;
    if (e.shiftKey && (document.activeElement === first || !root.contains(document.activeElement))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // Drag down to close: pointer events on the grab handle only (the buttons keep their clicks).
  let dragY0 = 0;
  let dy = $state(0);
  let dragging = $state(false);
  function down(e: PointerEvent) {
    if (!phone || (e.pointerType === "mouse" && e.button !== 0)) return;
    dragging = true;
    dragY0 = e.clientY;
    dy = 0;
    (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  }
  function move(e: PointerEvent) {
    if (dragging) dy = Math.max(0, e.clientY - dragY0);
  }
  function up(e: PointerEvent) {
    if (!dragging) return;
    dragging = false;
    const close = dragCloses(dy, root?.getBoundingClientRect().height ?? 0);
    dy = 0;
    (e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId);
    if (close) sheet.close();
  }

  /** A link inside the sheet: the sheet closes and its history entry becomes the destination. */
  function inSheetNav(e: MouseEvent, to: string) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    sheet.navigate(to);
  }

  const remaining = $derived(s.duration > 0 ? Math.max(0, s.duration - s.current) : 0);
  const status = $derived(j?.status ?? "");
</script>

<svelte:window {onkeydown} />

{#if sheet.open}
  {#if phone}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="scrim" data-testid="np-scrim" onclick={() => sheet.close()}></div>
  {/if}
  <div
    class="sheet"
    class:panel={!phone}
    class:dragging
    role="dialog"
    aria-modal={phone ? "true" : "false"}
    aria-label="Now playing"
    data-testid="now-playing"
    bind:this={root}
    style:transform={dy > 0 ? `translateY(${dy}px)` : undefined}
  >
    {#if phone}
      <div class="grab" data-testid="np-grab" onpointerdown={down} onpointermove={move} onpointerup={up} onpointercancel={up} role="presentation"><span class="handle"></span></div>
    {/if}
    <header>
      <button type="button" class="ico" bind:this={closeBtn} aria-label="Close now playing" data-testid="np-close" onclick={() => sheet.close()}><DownIcon size={24} /></button>
      <p class="from" data-testid="np-source">
        {#if s.source}Playing from <a href={s.source.href} onclick={(e) => inSheetNav(e, s.source?.href ?? "/")}>{s.source.label}</a>{:else}Now playing{/if}
      </p>
      <button type="button" class="ico" aria-label="Queue" aria-pressed={sheet.queueView} data-testid="np-queue-toggle" onclick={() => (sheet.queueView = !sheet.queueView)}><QueueIcon size={24} /></button>
    </header>

    <div class="scroll">
      {#if !trackId}
        <p class="muted">Nothing is playing.</p>
      {:else if sheet.queueView}
        <QueueList />
      {:else}
        <div class="meta">
          {#if !tape}<CoverArt {trackId} title="" size={112} />{/if}
          <div class="text">
            <h2 data-testid="np-title">{player.title}</h2>
            <p class="muted album">{player.album}</p>
            {#if s.noFlac}<span class="mp3chip">MP3</span>{/if}
          </div>
        </div>

        {#if s.error}<p role="alert" class="err">{s.error}</p>{/if}

        <Waveform {peaks} {bits} duration={s.duration} current={s.current} height={72} label="Seek" onseek={(t) => player.seek(t)} />
        <div class="times">
          <span data-testid="np-time">{fmt(s.current)}</span>
          <span class="muted">{s.duration > 0 ? `−${fmt(remaining)}` : ""}</span>
        </div>

        <div class="transport">
          <button type="button" class="sm" aria-label="Previous" onclick={() => player.prev()}><PrevIcon size={28} /></button>
          <button type="button" class="big" aria-label={s.playing ? "Pause" : "Play"} data-testid="np-play" onclick={() => player.toggle()}>{#if s.playing}<PauseIcon size={36} />{:else}<PlayIcon size={36} />{/if}</button>
          <button type="button" class="sm" aria-label="Next" onclick={() => player.next()} disabled={!player.hasNext}><NextIcon size={28} /></button>
        </div>

        {#if j}
          <div class="stars" data-testid="np-judge">
            <StarRating value={j.stars} onchange={(n) => judge.rate(trackId, n)} label="Your rating" size={52} />
          </div>
          <div class="chips"><StatusChips value={status} onchange={(st) => judge.setStatus(trackId, st)} /></div>
        {/if}

        <div class="acts">
          {#if !tape}<button type="button" aria-expanded={commenting} data-testid="np-comment" onclick={() => (commenting = !commenting)}>Comment at {fmt(s.current)}</button>{/if}
          {#if !tape}<a class="btn" href="/tracks/{trackId}" data-testid="np-open-take" onclick={(e) => inSheetNav(e, `/tracks/${encodeURIComponent(trackId)}`)}>Open take</a>{/if}
        </div>
        {#if commenting && !tape}
          <div class="composer" data-testid="np-composer">
            <CommentComposer {trackId} current={s.current} playing={true} oncreated={() => (commenting = false)} oncancel={() => (commenting = false)} />
          </div>
        {/if}
      {/if}
    </div>
  </div>
{/if}

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: calc(var(--z-sheet) - 1);
    background: rgb(0 0 0 / 0.55);
  }
  .sheet {
    position: fixed;
    z-index: var(--z-sheet);
    left: 0;
    right: 0;
    bottom: 0;
    max-height: 94dvh;
    display: flex;
    flex-direction: column;
    box-sizing: border-box;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-bottom: none;
    border-radius: var(--r-xl) var(--r-xl) 0 0;
    padding-bottom: var(--safe-b);
    transition: transform var(--dur-2) var(--ease-out);
    animation: rise var(--dur-3) var(--ease-out);
  }
  .sheet.dragging {
    transition: none;
  }
  @keyframes rise {
    from {
      transform: translateY(40%);
      opacity: 0.6;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sheet {
      animation: none;
      transition: none;
    }
  }
  .sheet.panel {
    left: auto;
    top: var(--topbar-h, 52px);
    bottom: var(--footer-h, 72px);
    width: var(--panel-w);
    max-height: none;
    border-radius: 0;
    border-bottom: 1px solid var(--line);
    border-right: none;
    animation: none;
  }
  .grab {
    display: grid;
    place-items: center;
    height: 28px;
    flex: none;
    touch-action: none;
    cursor: grab;
  }
  .handle {
    width: 44px;
    height: 4px;
    border-radius: var(--r-pill);
    background: var(--line-strong);
  }
  header {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: 0 var(--sp-3);
    flex: none;
  }
  .from {
    flex: 1;
    min-width: 0;
    margin: 0;
    text-align: center;
    font-size: var(--fs-sm);
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ico {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: var(--hit);
    height: var(--hit);
    padding: 0;
  }
  .scroll {
    overflow-y: auto;
    padding: var(--sp-3) var(--sp-4) var(--sp-5);
    display: grid;
    gap: var(--sp-3);
    align-content: start;
    overscroll-behavior: contain;
  }
  .meta {
    display: flex;
    gap: var(--sp-4);
    align-items: center;
  }
  .text {
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: var(--fs-lg, 1.25rem);
    overflow-wrap: anywhere;
  }
  .album {
    margin: var(--sp-1) 0 0;
  }
  .mp3chip {
    font-size: var(--fs-xs);
    padding: 0 var(--sp-1);
    border: 1px solid var(--warn);
    border-radius: var(--r-pill);
    color: var(--warn);
  }
  .err {
    color: var(--bad);
    margin: 0;
  }
  .times {
    display: flex;
    justify-content: space-between;
    font-variant-numeric: tabular-nums;
    font-family: ui-monospace, monospace;
  }
  .transport {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--sp-5);
  }
  .transport button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    border-radius: var(--r-pill);
  }
  .sm {
    width: var(--hit-lg);
    height: var(--hit-lg);
  }
  .big {
    width: var(--hit-xl);
    height: var(--hit-xl);
  }
  .stars {
    display: flex;
    justify-content: center;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    justify-content: center;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    justify-content: center;
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
  .composer {
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-3);
  }
</style>

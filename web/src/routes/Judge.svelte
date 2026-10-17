<script lang="ts">
  // Judge tab landing: one take at a time, big stars, auto-advance with Undo (wave 4b). The card is the player.
  import { onDestroy, untrack } from "svelte";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { createDuel } from "../lib/duelApi";
  import { newIdempotencyKey } from "../api/idem";
  import { inbox, reloadSeen } from "../lib/inbox.svelte";
  import { newSince } from "../lib/inbox";
  import { judge, type TrackStatus } from "../lib/judge.svelte";
  import { judgeCard } from "../lib/judgeCard.svelte";
  import {
    ADVANCE_MS,
    UNDO_MS,
    advanceAfterRating,
    append,
    currentTake,
    isDone,
    mediaMask,
    moveTo,
    needsMore,
    positionLabel,
    ratedMessage,
    skip as skipTake,
    sourceFromSearch,
    sourceHref,
    sourceLabel,
    startQueue,
    undoRating,
    type QueueState,
    type TimedSection,
    type UndoEntry,
  } from "../lib/judgeQueue";
  import { player } from "../lib/player.svelte";
  import { dismissToast, pushToast } from "../lib/toast.svelte";
  import JudgeCard from "../lib/components/JudgeCard.svelte";

  type Track = components["schemas"]["TrackSummary"];

  interface Props {
    /** window.location.search: ?from=new, ?song=<id> or ?muse=<id>; empty is "new unrated takes". */
    search: string;
    navigate: (to: string) => void;
  }
  let { search, navigate }: Props = $props();

  const PAGE = 50;
  const MUSE_SONGS = 20;

  const src = $derived(sourceFromSearch(search));
  const href = $derived(sourceHref(src));

  let st = $state<QueueState<Track>>(startQueue<Track>([]));
  let loading = $state(true);
  let error = $state("");
  let hasMore = $state(false);
  let name = $state("");
  let blind = $state(false);
  let cursor: string | null = null;
  let loadSeq = 0;

  const cur = $derived(currentTake(st));
  const done = $derived(isDone(st) && st.takes.length > 0);
  const ids = $derived(st.takes.map((t) => t.id));
  const label = $derived(sourceLabel(src, name));
  const stars = $derived(cur ? (judge.get(cur.id)?.stars ?? cur.stars) : null);
  const status = $derived(cur ? (judge.get(cur.id)?.status ?? cur.status) : "");

  // ---- source

  function seed(t: Track) {
    if (!judge.get(t.id)) judge.seed(t.id, { stars: t.stars, verdict: null, critic: null, status: t.status, tags: [] });
  }

  async function page(s: ReturnType<typeof sourceFromSearch>, after: string | null): Promise<{ items: Track[]; next: string | null } | null> {
    const c = after ? { cursor: after } : {};
    let q;
    if (s.kind === "new") q = { recorded_from: inbox.seenAt, sort: "recorded_asc" as const, limit: PAGE, ...c };
    else if (s.kind === "song") q = { song_id: s.id, sort: "recorded_asc" as const, limit: PAGE, ...c };
    else q = { unrated: true, sort: "recorded_desc" as const, limit: PAGE, ...c };
    const r = await api.GET("/api/tracks", { params: { query: q } });
    if (!r.data) return null;
    return { items: s.kind === "new" ? newSince(r.data.items, inbox.seenAt) : r.data.items, next: r.data.next_cursor };
  }

  async function musePage(id: string): Promise<Track[] | null> {
    const m = await api.GET("/api/muses/{id}", { params: { path: { id }, query: { songs_limit: 200 } } });
    if (!m.data) return null;
    name = m.data.muse.title;
    const out: Track[] = [];
    for (const song of m.data.songs.slice(0, MUSE_SONGS)) {
      const r = await api.GET("/api/tracks", { params: { query: { song_id: song.id, sort: "recorded_asc", limit: PAGE } } });
      if (r.data) out.push(...r.data.items);
    }
    return out;
  }

  async function load(s: ReturnType<typeof sourceFromSearch>) {
    const mine = ++loadSeq;
    loading = true;
    error = "";
    name = "";
    cursor = null;
    hasMore = false;
    st = startQueue<Track>([]);
    if (s.kind === "new") reloadSeen();
    try {
      let items: Track[] = [];
      if (s.kind === "muse") {
        const got = await musePage(s.id);
        if (got === null) throw new Error("muse");
        items = got;
      } else {
        const p = await page(s, null);
        if (p === null) throw new Error("page");
        items = p.items;
        cursor = p.next;
        hasMore = p.next !== null;
        if (s.kind === "song") name = items[0]?.song_title ?? "";
      }
      if (mine !== loadSeq) return;
      for (const t of items) seed(t);
      st = startQueue(items);
    } catch {
      if (mine === loadSeq) error = "Could not load the takes to judge";
    }
    if (mine === loadSeq) loading = false;
  }

  async function loadMore() {
    const after = cursor;
    if (!after) return;
    cursor = null; // one page request at a time
    const mine = loadSeq;
    const p = await page(src, after).catch(() => null);
    if (mine !== loadSeq) return;
    if (!p) {
      cursor = after;
      return;
    }
    for (const t of p.items) seed(t);
    st = append(st, p.items);
    cursor = p.next;
    hasMore = p.next !== null;
  }

  $effect(() => {
    void href;
    untrack(() => void load(src));
  });
  $effect(() => {
    if (needsMore(st, hasMore)) untrack(() => void loadMore());
  });

  // ---- what the current card needs besides the summary: sections (Jump to chorus), peaks, the Song's Pick

  let sections = $state<TimedSection[]>([]);
  let peaks = $state<number[] | null>(null);
  let bits = $state(8);
  let pickId = $state<string | null>(null);
  let extrasSeq = 0;
  const picks = new Map<string, string | null>(); // eslint-disable-line svelte/prefer-svelte-reactivity

  async function loadExtras(t: Track) {
    const mine = ++extrasSeq;
    sections = [];
    peaks = null;
    pickId = null;
    const [d, p, k] = await Promise.all([
      api.GET("/api/tracks/{id}", { params: { path: { id: t.id } } }).then((r) => r.data ?? null, () => null),
      api.GET("/api/tracks/{id}/peaks", { params: { path: { id: t.id }, query: { level: "overview" } } }).then((r) => r.data ?? null, () => null),
      pickOf(t),
    ]);
    if (mine !== extrasSeq) return;
    sections = d?.sections ?? [];
    peaks = p?.peaks.data ?? null;
    bits = p?.peaks.bits ?? 8;
    pickId = k;
  }

  /** The take that is the Song's Pick, when it is another take (Duel vs Pick needs two). */
  async function pickOf(t: Track): Promise<string | null> {
    if (!t.song_id || t.is_pick) return null;
    if (picks.has(t.song_id)) return picks.get(t.song_id) ?? null;
    const r = await api.GET("/api/tracks", { params: { query: { song_id: t.song_id, is_pick: true, limit: 1 } } }).then((x) => x.data ?? null, () => null);
    const id = r?.items[0]?.id ?? null;
    if (r) picks.set(t.song_id, id);
    return id && id !== t.id ? id : null;
  }

  $effect(() => {
    const t = cur;
    if (t) untrack(() => void loadExtras(t));
  });

  // ---- the card is the player

  $effect(() => {
    judgeCard.active = cur !== null;
  });
  // Blind: the lock screen shows "Take A", never the title, album or cover.
  $effect(() => {
    player.setMask(mediaMask(blind, ids, player.state.trackId));
  });
  // The card follows the player when it moves on by itself (the take ended and the next one of the list started).
  let lastTrack = untrack(() => player.state.trackId);
  $effect(() => {
    const id = player.state.trackId;
    if (id === lastTrack) return;
    lastTrack = id;
    untrack(() => (st = moveTo(st, id)));
  });

  function playTake(t: Track, startAt?: number) {
    player.play(t.id, ids, {
      ...(startAt !== undefined ? { startAt } : {}),
      ...(blind ? {} : { title: t.title }),
      source: { label, href },
    });
  }

  // ---- rating, auto-advance, undo

  let pending: { entry: UndoEntry; timer: ReturnType<typeof setTimeout> } | null = null;
  let toastId = 0;
  let writes: Promise<unknown> = Promise.resolve();

  /** Rating writes go out one after the other, so an Undo can never overtake the rating it undoes. */
  function write(id: string, n: number | null) {
    writes = writes.then(() => judge.rate(id, n));
  }

  function onrate(n: number | null) {
    const t = cur;
    if (!t) return;
    seed(t);
    const prev = pending && pending.entry.id === t.id ? pending.entry.prev : (judge.get(t.id)?.stars ?? null);
    if (pending) clearTimeout(pending.timer);
    pending = null;
    if (toastId) dismissToast(toastId);
    toastId = 0;
    write(t.id, n);
    if (n === null) return;
    const entry: UndoEntry = { id: t.id, prev, index: st.index };
    toastId = pushToast(ratedMessage(n), { kind: "success", ms: UNDO_MS, action: { label: "Undo", run: undo } });
    pending = { entry, timer: setTimeout(commit, ADVANCE_MS) };
  }

  function commit() {
    if (!pending) return;
    const e = pending.entry;
    pending = null;
    const wasPlaying = player.state.playing && player.state.trackId === e.id;
    st = advanceAfterRating(st, e);
    const next = currentTake(st);
    if (wasPlaying && next) playTake(next);
  }

  function undo() {
    if (pending) {
      clearTimeout(pending.timer);
      const e = pending.entry;
      pending = null;
      write(e.id, e.prev);
      return;
    }
    const r = undoRating(st);
    if (!r.entry) return;
    st = r.state;
    write(r.entry.id, r.entry.prev);
  }

  function onskip() {
    if (pending) return commit();
    st = skipTake(st);
  }

  async function duel() {
    const t = cur;
    const b = pickId;
    if (!t || !b) return;
    try {
      const d = await createDuel(api, t.id, b, newIdempotencyKey(), { blind: true });
      navigate(`/duels/${encodeURIComponent(d.id)}`);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "Could not create the duel");
    }
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.ctrlKey || e.metaKey || e.altKey || e.key < "1" || e.key > "5" || e.key.length !== 1) return;
    const el = e.target as HTMLElement | null;
    if (el && (el.isContentEditable || ["INPUT", "SELECT", "TEXTAREA"].includes(el.tagName) || el.closest('[data-testid="star-rating"]'))) return;
    e.preventDefault();
    onrate(Number(e.key));
  }

  onDestroy(() => {
    if (pending) clearTimeout(pending.timer);
    judgeCard.active = false;
    player.setMask(null);
  });
</script>

<svelte:window {onkeydown} />

<section class="judge" data-testid="judge">
  <div class="head">
    <p class="progress" data-testid="judge-progress" role="status">{label}{#if st.takes.length > 0 && !done} · {positionLabel(st, hasMore)}{/if}</p>
    <button type="button" class="blind" aria-pressed={blind} data-testid="judge-blind" onclick={() => (blind = !blind)}>Blind</button>
  </div>
  {#if st.takes.length > 1}
    <div class="dots" aria-hidden="true">
      {#each st.takes.slice(0, 20) as t, i (t.id)}<span class:on={i < st.index} class:cur={i === st.index}></span>{/each}
    </div>
  {/if}

  {#if error}
    <p role="alert">{error} <button type="button" onclick={() => void load(src)}>Retry</button></p>
  {:else if loading}
    <p class="muted" aria-busy="true">Loading takes…</p>
  {:else if cur}
    {#key cur.id}
      <JudgeCard
        take={cur}
        index={st.index}
        {blind}
        {sections}
        {peaks}
        {bits}
        {stars}
        {status}
        canDuel={pickId !== null}
        play={(at) => playTake(cur, at)}
        {onrate}
        onstatus={(s: TrackStatus) => void judge.setStatus(cur.id, s)}
        {onskip}
        onduel={() => void duel()}
      />
    {/key}
  {:else if done}
    <p data-testid="judge-done">All done: {st.rated} rated.</p>
    <p><a class="btn" href="/">Back to the Library</a></p>
  {:else}
    <p class="muted" data-testid="judge-empty">Nothing to judge here. {src.kind === "unrated" ? "Every take has a rating." : ""}</p>
  {/if}
</section>

<style>
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
    margin-bottom: var(--sp-2);
  }
  .progress {
    margin: 0;
    color: var(--text-2);
  }
  .blind {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
  }
  .blind[aria-pressed="true"] {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent);
    font-weight: var(--fw-bold);
  }
  .dots {
    display: flex;
    gap: var(--sp-1);
    justify-content: center;
    margin-bottom: var(--sp-3);
  }
  .dots span {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    border: 1px solid var(--line-strong);
  }
  .dots span.on {
    background: var(--accent);
    border-color: var(--accent);
  }
  .dots span.cur {
    border-color: var(--accent);
  }
  .btn {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    color: var(--text);
    text-decoration: none;
  }
</style>

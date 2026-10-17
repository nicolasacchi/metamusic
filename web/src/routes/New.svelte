<script lang="ts">
  import { untrack } from "svelte";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { approvals, refreshApprovals } from "../lib/approvals.svelte";
  import {
    buildInbox,
    headerLine,
    countNew,
    newlyCompleted,
    newSince,
    soonestJob,
    sourceLabel,
    type InboxModel,
  } from "../lib/inbox";
  import { heardVersion, inbox, markAllSeen, refreshInboxBadge, reloadSeen } from "../lib/inbox.svelte";
  import { player, type PlayOptions } from "../lib/player.svelte";
  import { routeReady } from "../lib/scrollRestore";
  import FlightCard from "../lib/components/inbox/FlightCard.svelte";
  import InboxRow from "../lib/components/inbox/InboxRow.svelte";
  import PendingApprovals from "../lib/components/PendingApprovals.svelte";
  import PlayIcon from "../lib/components/icons/PlayIcon.svelte";
  import { formatMicroUSD } from "../lib/money";

  type Track = components["schemas"]["TrackSummary"];
  type Batch = components["schemas"]["Batch"];
  type Job = components["schemas"]["JobSummary"];

  const PAGE = 50;
  const MAX_BATCHES = 6;

  let tracks = $state<Track[]>([]);
  let trackTotal = $state(0);
  let nextCursor = $state<string | null>(null);
  let unheardProxy = $state<Track[]>([]);
  let batches = $state<Batch[]>([]);
  let failures = $state<Job[]>([]);
  let proposalSong = $state<string | null>(null);
  let loading = $state(true);
  let error = $state("");
  let now = $state(new Date());
  let ready = false;
  let seq = 0;

  interface Toast {
    id: number;
    trackId: string;
    title: string;
  }
  let toasts = $state<Toast[]>([]);
  let toastSeq = 0;

  // `heardVersion.n` is read so that a take heard while this page is open loses its dot at once.
  const model = $derived.by<InboxModel>(() => {
    void heardVersion.n;
    return buildInbox({
      tracks,
      trackTotal,
      seenAt: inbox.seenAt,
      heard: inbox.heard,
      unheardProxy,
      batches,
      approvals: approvals.items.length,
      openDuels: inbox.duels,
      proposals: inbox.proposals,
      failures,
    });
  });
  const unheardNew = $derived(model.rows.filter((r) => r.unheard).length);

  $effect(() => {
    const t = setInterval(() => (now = new Date()), 30_000);
    return () => clearInterval(t);
  });

  async function loadBatches(): Promise<Batch[]> {
    const l = await api.GET("/api/batches", { params: { query: { state: ["active"], limit: MAX_BATCHES } } });
    // a batch that just finished is no longer listed as active: re-read the ones we were showing so its last take is seen
    const all = [...(l.data ? l.data.items.map((b) => b.id) : []), ...batches.map((b) => b.id)];
    const ids = all.filter((id, i) => all.indexOf(id) === i);
    const got = await Promise.all(ids.map((id) => api.GET("/api/batches/{id}", { params: { path: { id } } }).then((r) => r.data ?? null, () => null)));
    return got.filter((b): b is Batch => b !== null);
  }

  /** Reads everything; `quiet` keeps the page as is while it refreshes (live updates). */
  async function load(quiet = false) {
    const mine = ++seq;
    if (!quiet) loading = true;
    reloadSeen();
    const seen = inbox.seenAt;
    const [t, b, s, j, u] = await Promise.allSettled([
      api.GET("/api/tracks", { params: { query: { recorded_from: seen, sort: "recorded_asc", limit: PAGE } } }),
      loadBatches(),
      api.GET("/api/songs", { params: { query: { has_open_proposals: true, limit: 1 } } }),
      api.GET("/api/jobs", { params: { query: { state: ["failed"], since: seen, limit: 20 } } }),
      api.GET("/api/tracks", { params: { query: { unrated: true, status: ["draft"], sort: "recorded_desc", limit: 30 } } }),
      refreshApprovals(),
      api.GET("/api/duels", { params: { query: { state: ["open"], limit: 1 } } }).then((r) => {
        if (r.data) inbox.duels = r.data.total;
      }),
    ]);
    if (mine !== seq) return;
    error = "";
    if (t.status === "fulfilled" && t.value.data) {
      tracks = t.value.data.items;
      trackTotal = t.value.data.total;
      nextCursor = t.value.data.next_cursor;
      inbox.newCount = countNew(tracks, trackTotal, seen);
    } else if (!quiet) error = "Could not load new takes";
    if (b.status === "fulfilled") {
      const before = untrack(() => batches);
      const after = b.value;
      announce(newlyCompleted(before, after), after);
      batches = after.filter((x) => x.state === "active" || x.jobs.some((jb) => !["completed", "failed", "cancelled", "expired"].includes(jb.state)));
    }
    if (s.status === "fulfilled" && s.value.data) {
      inbox.proposals = s.value.data.total;
      proposalSong = s.value.data.items[0]?.id ?? null;
    }
    if (j.status === "fulfilled" && j.value.data) failures = j.value.data.items;
    if (u.status === "fulfilled" && u.value.data) unheardProxy = u.value.data.items;
    loading = false;
    if (!ready) {
      ready = true;
      void routeReady();
    }
  }

  function announce(done: Job[], after: Batch[]) {
    void after;
    for (const jb of done) {
      if (!jb.track_id) continue;
      const toast = { id: ++toastSeq, trackId: jb.track_id, title: jb.title || "Take" };
      toasts = [...toasts, toast];
      setTimeout(() => dismiss(toast.id), 12_000);
    }
  }
  function dismiss(id: number) {
    toasts = toasts.filter((x) => x.id !== id);
  }

  // untracked: load() reads the batches it refreshes, which would re-run this effect forever
  $effect(() =>
    untrack(() => {
      void load();
      void refreshInboxBadge();
    }),
  );

  // Live updates while the page is visible: wait_for_job on the take furthest along, else a slow look for new batches.
  $effect(() => {
    let stop = false;
    const ac = new AbortController();
    const sleep = (ms: number) => new Promise<void>((res) => setTimeout(res, ms));
    async function loop() {
      while (!stop) {
        if (document.hidden) {
          await new Promise<void>((res) => document.addEventListener("visibilitychange", () => res(), { once: true }));
          continue;
        }
        const job = soonestJob(untrack(() => batches));
        if (!job) {
          await sleep(8000);
          if (!stop && !document.hidden) await load(true);
          continue;
        }
        try {
          await api.GET("/api/jobs/{id}/wait", { params: { path: { id: job.id }, query: { since_state: job.state, timeout_s: 25 } }, signal: ac.signal });
        } catch {
          if (stop) return;
          await sleep(3000);
        }
        if (!stop) await load(true);
      }
    }
    void loop();
    return () => {
      stop = true;
      ac.abort();
    };
  });

  // ---- playing

  /** "Playing from <label>" links back here; an explicit replacement of a loaded queue offers Undo. */
  function opts(base: Omit<PlayOptions, "source"> & { source: string }): PlayOptions {
    const { source, ...rest } = base;
    return { ...rest, source: { label: source, href: "/new" }, undo: true };
  }
  function moreNew(): () => Promise<string[]> {
    let cursor = nextCursor;
    const seen = inbox.seenAt;
    return async () => {
      if (!cursor) return [];
      const r = await api.GET("/api/tracks", { params: { query: { recorded_from: seen, sort: "recorded_asc", limit: PAGE, cursor } } });
      if (!r.data) throw new Error("page failed");
      cursor = r.data.next_cursor;
      return newSince(r.data.items, seen).map((x) => x.id);
    };
  }
  function playIn(ids: string[], id: string, source: string, more?: () => Promise<string[]>) {
    const t = [...tracks, ...unheardProxy].find((x) => x.id === id);
    player.play(id, ids, opts({ ...(t ? { title: t.title } : {}), source, ...(more ? { more } : {}) }));
  }
  const newLabel = $derived(sourceLabel(inbox.seenAt, now));
  function playAll() {
    const first = model.playOrder[0];
    if (first) playIn(model.playOrder, first, newLabel, nextCursor ? moreNew() : undefined);
  }
  function playRowNew(id: string) {
    playIn(model.playOrder, id, newLabel, nextCursor ? moreNew() : undefined);
  }
  function playRowUnheard(id: string) {
    playIn(model.stillUnheard.map((r) => r.track.id), id, "Still unheard");
  }
  function playToast(t: Toast) {
    player.play(t.trackId, [t.trackId], opts({ title: t.title, source: "Take ready" }));
    dismiss(t.id);
  }

  async function markSeen() {
    const newest = tracks.reduce((m, t) => Math.max(m, Date.parse(t.recorded_at)), 0);
    markAllSeen(new Date(Math.max(Date.now(), newest)));
    await load(true);
    void refreshInboxBadge();
  }

  const costOf = (b: Batch) => `${formatMicroUSD(b.cost.settled_micro_usd + b.cost.outstanding_micro_usd)} of up to ${formatMicroUSD(b.cost.ceiling_micro_usd)}`;
</script>

<h1>New</h1>
<p class="since" data-testid="new-header">{headerLine(model, inbox.seenAt, now)}</p>
<div class="actions">
  <button type="button" class="primary" disabled={model.playOrder.length === 0} onclick={playAll} data-testid="play-all-new">
    <PlayIcon size={22} /> Play all new{model.count > 0 ? ` (${model.count})` : ""}
  </button>
  <button type="button" onclick={() => void markSeen()} disabled={model.count === 0 && model.failures.length === 0} data-testid="mark-all-seen">Mark all seen</button>
</div>
{#if error}<p role="alert">{error}</p>{/if}

{#if toasts.length > 0}
  <div class="toasts" role="status" aria-live="polite">
    {#each toasts as t (t.id)}
      <div class="toast" data-testid="take-ready">
        Take ready · {t.title}
        <button type="button" onclick={() => playToast(t)} data-testid="take-ready-play">Play</button>
        <button type="button" aria-label="Dismiss" onclick={() => dismiss(t.id)}>×</button>
      </div>
    {/each}
  </div>
{/if}

{#if model.inFlight.length > 0}
  <section aria-labelledby="flight-h" data-testid="inbox-flight">
    <h2 id="flight-h">In flight <a class="all" href="/jobs">Jobs ›</a></h2>
    {#each model.inFlight as f (f.id)}
      {@const b = batches.find((x) => x.id === f.id)}
      <FlightCard batch={f} cost={b ? costOf(b) : ""} />
    {/each}
  </section>
{/if}

{#if model.needsYou.total > 0}
  <section aria-labelledby="needs-h" data-testid="inbox-needs" data-count={model.needsYou.total}>
    <h2 id="needs-h">Needs you</h2>
    <PendingApprovals />
    {#if model.needsYou.duels > 0}
      <p class="need" data-testid="need-duels"><span>{model.needsYou.duels} open duel{model.needsYou.duels === 1 ? "" : "s"}</span> <a class="btn" href="/duels">Start</a></p>
    {/if}
    {#if model.needsYou.proposals > 0}
      <p class="need" data-testid="need-proposals">
        <span>{model.needsYou.proposals} Pick proposal{model.needsYou.proposals === 1 ? "" : "s"} from agents</span>
        <a class="btn" href={model.needsYou.proposals === 1 && proposalSong ? `/songs/${proposalSong}` : "/board"}>Open</a>
      </p>
    {/if}
  </section>
{/if}

<section aria-labelledby="new-h" data-testid="inbox-new">
  <h2 id="new-h">New since {new Date(inbox.seenAt).toLocaleDateString(undefined, { weekday: "short" })}</h2>
  <ul aria-busy={loading}>
    {#each model.rows as r (r.track.id)}
      <InboxRow track={r.track} unheard={r.unheard} playing={r.track.id === player.state.trackId} {now} onplay={playRowNew} />
    {:else}
      {#if !loading}<li class="muted empty" data-testid="new-empty">Nothing new. {unheardNew === 0 && model.count === 0 ? "You are up to date." : ""}</li>{/if}
    {/each}
  </ul>
  {#if model.partial}<p class="muted">Showing the oldest {tracks.length} of {model.count}.</p>{/if}
</section>

<details class="unheard" data-testid="inbox-unheard">
  <summary>Still unheard ({model.stillUnheard.length}{unheardProxy.length >= 30 ? "+" : ""})</summary>
  <ul>
    {#each model.stillUnheard as r (r.track.id)}
      <InboxRow track={r.track} unheard={true} playing={r.track.id === player.state.trackId} {now} onplay={playRowUnheard} />
    {/each}
  </ul>
</details>

{#if model.failures.length > 0}
  <section aria-labelledby="fail-h" data-testid="inbox-failures">
    <h2 id="fail-h">Failures since</h2>
    <ul>
      {#each model.failures as f (f.id)}
        <li class="fail" data-testid="failure-row"><a href="/jobs/{f.id}">{f.title || f.id}</a> <code>{f.error_code ?? "FAILED"}</code></li>
      {/each}
    </ul>
  </section>
{/if}

<style>
  .since {
    margin: 0 0 var(--sp-3);
    color: var(--text-2);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    margin-bottom: var(--sp-4);
  }
  .primary {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--sp-2);
    flex: 1 1 14rem;
    min-height: var(--hit-lg);
    background: var(--accent);
    color: var(--accent-ink);
    border: none;
    border-radius: var(--r-md);
    font-weight: var(--fw-bold);
    font-size: var(--fs-lg);
    cursor: pointer;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .actions button:not(.primary) {
    min-height: var(--hit);
  }
  h2 {
    font-size: var(--fs-sm);
    text-transform: uppercase;
    letter-spacing: var(--tracking-label);
    color: var(--text-2);
    margin: var(--sp-5) 0 var(--sp-2);
    display: flex;
    justify-content: space-between;
  }
  .all {
    text-transform: none;
    letter-spacing: 0;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .need {
    display: flex;
    justify-content: space-between;
    align-items: center;
    min-height: var(--hit);
    margin: var(--sp-2) 0;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    color: var(--text);
    text-decoration: none;
  }
  .unheard {
    margin-top: var(--sp-5);
  }
  .unheard summary {
    min-height: var(--hit);
    display: flex;
    align-items: center;
    cursor: pointer;
    color: var(--text-2);
  }
  .empty,
  .fail {
    min-height: var(--hit);
    display: flex;
    align-items: center;
    gap: var(--sp-2);
  }
  .toasts {
    position: sticky;
    top: var(--sp-2);
    z-index: var(--z-toast);
    display: grid;
    gap: var(--sp-2);
    margin-bottom: var(--sp-3);
  }
  .toast {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    padding: var(--sp-2) var(--sp-3);
    background: var(--accent-soft);
    border: 1px solid var(--accent);
    border-radius: var(--r-md);
  }
  .toast button {
    min-height: var(--hit);
    min-width: var(--hit);
  }
</style>

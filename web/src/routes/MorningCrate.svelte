<script lang="ts">
  // One Morning Crate (M6N-W2, C384.3): blind listening and rating until the reveal, then the verdicts. Ratings, markers and
  // skips go through the morning outbox (idempotent event ids), so a bad connection on the commute never loses or doubles one.
  import { onDestroy } from "svelte";
  import { api } from "../api/client";
  import { OfflineError } from "../api/offline";
  import { morningOutbox, onMorningResult } from "../api/outbox";
  import MorningPlayer from "../lib/components/MorningPlayer.svelte";
  import MorningReveal from "../lib/components/MorningReveal.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { can } from "../lib/me.svelte";
  import { offline } from "../lib/offline.svelte";
  import { player } from "../lib/player.svelte";
  import { uuidv7 } from "../lib/id";
  import { crateName, rateEvent, settleBeforeReveal, shouldReloadAfterAnswer, type MorningCrate } from "../lib/morningCrate";

  interface Props {
    id: string;
    navigate: (to: string) => void;
  }
  let { id, navigate }: Props = $props();

  let crate = $state<MorningCrate | null>(null);
  let error = $state("");
  let stars = $state<Record<number, number | null>>({});
  let revealing = $state(false);
  let revealError = $state("");
  let offlineBusy = $state(false);
  let offlineError = $state("");

  async function load() {
    try {
      const r = await api.GET("/api/morning-crates/{id}", { params: { path: { id } } });
      if (r.data) {
        crate = r.data;
        error = "";
        // what the server now has no longer needs the optimistic copy
        for (const c of r.data.chapters) if (c.n in stars && stars[c.n] === c.stars) delete stars[c.n];
        player.updateCrate(r.data);
        void offline.refreshMorning(r.data.id);
      } else error = r.error?.message ?? "Could not load the Morning Crate";
    } catch {
      error = crate ? error : "Could not load the Morning Crate";
    }
  }

  $effect(() => {
    void load();
  });

  // a crate that is still being built is polled until it is ready
  $effect(() => {
    if (crate?.state !== "building") return;
    const h = setInterval(() => void load(), 3000);
    return () => clearInterval(h);
  });

  // the server answered a batch of this crate: it may have revealed it (every chapter rated)
  const off = onMorningResult((r) => {
    if (shouldReloadAfterAnswer(r, id, crate)) void load();
  });
  onDestroy(off);

  function rate(chapter: number, v: number | null) {
    if (!crate) return;
    const e = rateEvent(crate, chapter, v, uuidv7(), new Date());
    if (!e) return;
    stars[chapter] = v;
    void morningOutbox.append(e);
  }

  async function reveal() {
    if (revealing || !crate) return;
    revealing = true;
    revealError = "";
    try {
      await settleBeforeReveal(player.crate, morningOutbox);
      const r = await api.POST("/api/morning-crates/{id}/reveal", { params: { path: { id } } });
      if (r.data) {
        crate = r.data;
        player.updateCrate(r.data);
        pushToast("Revealed. The titles and your verdicts are below.");
      } else revealError = r.error?.message ?? "Could not reveal the crate";
    } catch {
      revealError = "Could not reveal the crate";
    } finally {
      revealing = false;
    }
  }

  async function makeOffline() {
    if (!crate || offlineBusy) return;
    offlineBusy = true;
    offlineError = "";
    try {
      await load(); // fresh links
      if (crate) await offline.downloadMorning(crate);
      pushToast("The Morning Crate is on this device.");
    } catch (e) {
      offlineError = e instanceof OfflineError ? e.message : "Offline download failed";
    } finally {
      offlineBusy = false;
    }
  }

  const sideProgress = $derived(
    Object.entries(offline.morningProgress)
      .map(([s, p]) => (p.total > 0 ? `${Math.round((p.done / p.total) * 100)}%` : `side ${Number(s) + 1}`))
      .join(", "),
  );
  const allOffline = $derived(!!crate && crate.sides.filter((s) => !s.removed).every((s) => offline.morningSides.includes(s.side)));
</script>

<section data-testid="morning-crate-page">
  {#if error}
    <h1>Morning crate</h1>
    <p role="alert" data-testid="morning-crate-error">{error}</p>
  {:else if !crate}
    <h1>Morning crate</h1>
    <p class="muted" role="status">Loading…</p>
  {:else}
    <h1>{crateName(crate)}</h1>
    {#if crate.state === "building"}
      <p class="muted" role="status" data-testid="morning-building">Building the sides. This takes a minute or two.</p>
    {:else if crate.state === "empty"}
      <p data-testid="morning-crate-empty">That day had nothing unjudged to put in a crate.</p>
    {:else if crate.state === "failed"}
      <p role="alert" data-testid="morning-crate-failed">The build failed{crate.error_code ? ` (${crate.error_code})` : ""}. Build another one from the list.</p>
    {:else}
      <p class="muted" data-testid="morning-crate-meta">
        {crate.takes} takes, {Math.round(crate.duration_ms / 60_000)} minutes, {crate.sides.length} {crate.sides.length === 1 ? "side" : "sides"}.
        {crate.revealed ? "Revealed." : "Blind: no titles until every take is rated or you reveal it."}
        {#if crate.adjacency_relaxed}Two takes of one lineage could not be kept apart.{/if}
      </p>
      <MorningPlayer {crate} {stars} onrate={rate} />
      {#if !crate.revealed}
        <p>
          <button type="button" class="btn" disabled={revealing} onclick={() => void reveal()} data-testid="morning-reveal-now">Reveal now</button>
          <span class="muted">Shows the titles and saves your ratings, markers and skips on the takes.</span>
        </p>
        {#if revealError}<p role="alert" data-testid="morning-reveal-error">{revealError}</p>{/if}
      {:else}
        <MorningReveal {crate} {navigate} />
      {/if}
      {#if offline.available && can("get_offline_set")}
        <h2>On this device</h2>
        <p>
          <button type="button" class="btn" disabled={offlineBusy} onclick={() => void makeOffline()} data-testid="morning-offline">
            {offlineBusy ? `Downloading${sideProgress ? ` ${sideProgress}` : "…"}` : allOffline ? "Download again" : "Make offline"}
          </button>
          {#if allOffline}<span class="muted" data-testid="morning-offline-state">Offline. No title is stored with the files.</span>{/if}
        </p>
        <p class="muted">Only the newest crate is kept on the phone; making this one offline removes the previous one's sides.</p>
        {#if offlineError}<p role="alert" data-testid="morning-offline-error">{offlineError}</p>{/if}
      {/if}
    {/if}
  {/if}
</section>

<style>
  p {
    margin: var(--sp-2) 0;
  }
  .btn {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--accent);
    border-radius: var(--r-pill);
    background: transparent;
    color: var(--accent);
  }
</style>

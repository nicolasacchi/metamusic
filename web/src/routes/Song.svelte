<script lang="ts">
  import { routeReady } from "../lib/scrollRestore";
  import { untrack } from "svelte";
  import { api } from "../api/client";
  import { newIdempotencyKey } from "../api/idem";
  import type { components } from "../api/schema";
  import { createDuel } from "../lib/duelApi";
  import { draftsIntent, mayGenerate, startGeneration, type Viewer } from "../lib/genIntent";
  import { judge } from "../lib/judge.svelte";
  import MusePicker from "../lib/components/MusePicker.svelte";
  import PickHistory from "../lib/components/PickHistory.svelte";
  import ProposalList from "../lib/components/ProposalList.svelte";
  import TakeCard from "../lib/components/TakeCard.svelte";
  import TempoBadge from "../lib/components/TempoBadge.svelte";
  import {
    PENDING_REFETCH_MS,
    acceptProposalRequest,
    dismissProposalRequest,
    orderTakes,
    pendingDelay,
    selectForDuel,
  } from "../lib/songView";
  import { tokenLabel } from "../lib/soundsView";
  import { fetchViewer } from "../lib/viewer";

  type View = components["schemas"]["SongView"];
  interface Props {
    id: string;
    navigate?: (to: string, replace?: boolean) => void;
    /** Pre-loaded view (unit tests; the page otherwise fetches it). */
    initial?: View;
    viewer?: Viewer | null;
  }
  let { id, navigate, initial, viewer = null }: Props = $props();

  function go(to: string) {
    if (navigate) navigate(to);
    else {
      history.pushState(null, "", to);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  }

  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
  const human = $derived(me !== null && me.kind === "human");

  // svelte-ignore state_referenced_locally
  let view = $state<View | null>(initial ?? null);
  let error = $state("");
  let note = $state("");
  let selected = $state<string[]>([]);
  let editing = $state(false);
  let titleDraft = $state("");
  let angleDraft = $state("");
  let pickingMuse = $state(false);
  let angles = $state<string[]>([]);
  let attempts = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;

  const takes = $derived(view ? orderTakes(view.takes) : []);
  const queue = $derived(takes.map((t) => t.id));
  const titleOf = (tid: string) => view?.takes.find((t) => t.id === tid)?.title ?? tid;

  async function load(quiet = false) {
    const mine = ++seq;
    if (!quiet) error = "";
    try {
      const r = await api.GET("/api/songs/{id}", { params: { path: { id }, query: { include_events: 10 } } });
      if (mine !== seq) return;
      if (!r.data) {
        if (!quiet) error = r.response.status === 404 ? "Song not found" : (r.error?.message ?? "Failed to load the Song");
        return;
      }
      view = r.data;
      for (const t of r.data.takes) {
        if (!judge.get(t.id)) judge.seed(t.id, { stars: t.stars, verdict: null, critic: t.critic_stars === null ? null : { stars: t.critic_stars, verdict: null }, status: t.status, tags: [] });
      }
    } catch {
      if (!quiet && mine === seq) error = "Failed to load the Song";
    } finally {
      if (mine === seq) void routeReady();
    }
  }

  // Route change: reload. A queued recompute (`pending`) is polled at most three times, 1.5 s apart.
  $effect(() => {
    void id;
    untrack(() => {
      attempts = 0;
      if (!initial) view = null;
      selected = [];
      if (!initial) void load();
    });
  });
  $effect(() => {
    const delay = pendingDelay(view?.pick?.pending === true || view?.pending === true, attempts);
    if (delay === null) return;
    timer = setTimeout(() => {
      attempts++;
      void load(true);
    }, delay);
    return () => clearTimeout(timer);
  });
  // Board angles for the datalist (once, human or agent alike).
  $effect(() => {
    if (initial) return;
    void api.GET("/api/board", { params: { query: { detail: "counts", limit: 1 } } }).then((r) => {
      angles = (r.data?.columns ?? []).flatMap((c) => (c.angle ? [c.angle] : []));
    }).catch(() => {});
  });

  /** A rating or status change can move the Pick: refetch shortly, then follow `pending`. */
  function changed() {
    attempts = 0;
    clearTimeout(timer);
    timer = setTimeout(() => void load(true), PENDING_REFETCH_MS);
  }

  async function fail(r: { response: Response; error?: { message?: string } }, fallback: string) {
    note = r.error?.message ?? fallback;
    if (r.response.status === 409) await load(true);
  }

  async function pin(takeId: string | null, proposalId?: string) {
    note = "";
    try {
      const r = await api.PUT("/api/songs/{id}/pin", { params: { path: { id } }, body: { take_id: takeId, ...(proposalId ? { proposal_id: proposalId } : {}) } });
      if (!r.data) return void (await fail(r, "Could not change the Pick"));
      await load(true);
    } catch {
      note = "Could not change the Pick";
    }
  }
  async function accept(p: components["schemas"]["PickProposal"]) {
    const q = acceptProposalRequest(p);
    await pin(q.body.take_id, q.body.proposal_id);
  }
  async function dismiss(p: components["schemas"]["PickProposal"]) {
    const q = dismissProposalRequest(p);
    note = "";
    try {
      const r = await api.POST("/api/pick-proposals/{id}/dismiss", { params: { path: { id: q.proposalId } } });
      if (!r.data) return void (await fail(r, "Could not dismiss the proposal"));
      await load(true);
    } catch {
      note = "Could not dismiss the proposal";
    }
  }

  async function patch(body: Omit<components["schemas"]["UpdateSongInBody"], "revision">): Promise<boolean> {
    if (!view) return false;
    note = "";
    try {
      const r = await api.PATCH("/api/songs/{id}", { params: { path: { id } }, body: { ...body, revision: view.song.revision } });
      if (!r.data) {
        await fail(r, "Could not save the change");
        return false;
      }
      await load(true);
      return true;
    } catch {
      note = "Could not save the change";
      return false;
    }
  }
  function startEdit() {
    if (!view) return;
    titleDraft = view.song.title;
    angleDraft = view.song.angle ?? "";
    editing = true;
  }
  async function saveEdit(e: Event) {
    e.preventDefault();
    if (!view) return;
    const body: Omit<components["schemas"]["UpdateSongInBody"], "revision"> = {};
    if (titleDraft.trim() && titleDraft.trim() !== view.song.title) body.title = titleDraft.trim();
    const a = angleDraft.trim();
    if (a !== (view.song.angle ?? "")) body.angle = a === "" ? null : a;
    if (Object.keys(body).length === 0 || (await patch(body))) editing = false;
  }

  let duelKey = "";
  let duelFor = "";
  async function startDuel() {
    if (selected.length !== 2) return;
    const [a, b] = selected as [string, string];
    if (duelFor !== a + b) {
      duelKey = newIdempotencyKey();
      duelFor = a + b;
    }
    note = "";
    try {
      const d = await createDuel(api, a, b, duelKey, { blind: true });
      go(`/duels/${encodeURIComponent(d.id)}`);
    } catch (err) {
      note = err instanceof Error ? err.message : "Could not start the duel";
    }
  }

  function generate() {
    if (view?.draft) startGeneration(sessionStorage, draftsIntent([view.draft.id]), (to) => location.assign(to));
  }
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a href="/board">Back to the Board</a></p>
{:else if !view}
  <p class="muted">Loading…</p>
{:else}
  {@const s = view.song}
  <article data-testid="song-page" data-song-id={s.id}>
    {#if s.retired}
      <p role="status" data-testid="song-retired">This Song was merged{#if s.retired_into} into <a href="/songs/{encodeURIComponent(s.retired_into)}">another Song</a>{/if}.</p>
    {/if}
    <header>
      {#if editing}
        <form class="edit" onsubmit={saveEdit}>
          <label>Title <input bind:value={titleDraft} maxlength="200" required data-testid="song-title-input" /></label>
          <label>Angle <input bind:value={angleDraft} list="song-angles" maxlength="80" data-testid="song-angle-input" /></label>
          <datalist id="song-angles">{#each angles as a (a)}<option value={a}></option>{/each}</datalist>
          <button type="submit" data-testid="song-save">Save</button>
          <button type="button" onclick={() => (editing = false)}>Cancel</button>
        </form>
      {:else}
        <h1 data-testid="song-title">{s.title}</h1>
        {#if !s.retired}<button type="button" onclick={startEdit} data-testid="song-edit">Edit</button>{/if}
      {/if}
      <p class="chips">
        <span class="state" data-testid="song-state">{s.state}</span>
        {#if s.angle}<span class="chip" data-testid="song-angle">{s.angle}</span>{/if}
        {#if s.queued_jobs > 0}<span class="chip">queued ×{s.queued_jobs}</span>{/if}
        {#if view.pending || view.pick?.pending}<span class="chip" data-testid="song-pending">updating…</span>{/if}
        <TempoBadge tempo={view.sounds?.tempo} />
        {#each Object.entries(view.sounds?.facets ?? {}) as [facet, entries] (facet)}
          {#each entries ?? [] as e (e.value)}
            <a class="chip" href="/sounds?sound={encodeURIComponent(`${facet}:${e.value}`)}">{tokenLabel(`${facet}:${e.value}`)}</a>
          {/each}
        {/each}
        {#each view.sessions ?? [] as ses (ses.id)}
          <a class="chip" href="/board?session={encodeURIComponent(ses.id)}" data-testid="song-session">{ses.name}</a>
        {/each}
      </p>
      <p class="meta">
        Muse:
        {#if view.muse}
          <a href="/muses/{encodeURIComponent(view.muse.id)}" data-testid="song-muse">{view.muse.title}{#if view.muse.artist} · {view.muse.artist}{/if}</a>
          {#if human && !s.retired}<button type="button" onclick={() => patch({ muse_id: null })} data-testid="song-muse-clear">Clear</button>{/if}
        {:else}
          <span class="muted">none</span>
        {/if}
        {#if human && !s.retired}
          <button type="button" onclick={() => (pickingMuse = !pickingMuse)} data-testid="song-muse-change">{view.muse ? "Change" : "Assign"}</button>
          <button type="button" aria-pressed={s.hidden} onclick={() => patch({ hidden: !s.hidden })} data-testid="song-hide">{s.hidden ? "Unhide" : "Hide"}</button>
        {/if}
      </p>
      {#if pickingMuse}
        <MusePicker
          onpick={async (m) => {
            if (await patch({ muse_id: m.id })) pickingMuse = false;
          }}
          oncancel={() => (pickingMuse = false)}
        />
      {/if}
    </header>

    {#if note}<p role="alert" data-testid="song-note">{note}</p>{/if}

    {#if view.draft && takes.length === 0}
      <section data-testid="song-draft" aria-labelledby="draft-h">
        <h2 id="draft-h">Draft</h2>
        <p><a href="/drafts/{encodeURIComponent(view.draft.id)}">{view.draft.label}</a> · {view.draft.status}</p>
        {#if mayGenerate(me)}<button type="button" onclick={generate} data-testid="song-generate">Generate…</button>{/if}
      </section>
    {/if}

    {#if takes.length > 0}
      <section aria-labelledby="takes-h">
        <h2 id="takes-h">Takes <span class="muted">({view.takes_total})</span></h2>
        {#if human}
          <p class="duelbar">
            <button type="button" disabled={selected.length !== 2} onclick={startDuel} data-testid="duel-start">Blind duel{selected.length === 2 ? "" : ` (${selected.length}/2 selected)`}</button>
          </p>
        {/if}
        <div class="cards">
          {#each takes as t (t.id)}
            <TakeCard
              take={t}
              {queue}
              pickSource={view.pick?.source}
              pickEvidence={view.pick?.evidence}
              canPin={human && !s.retired}
              pinIneligible={view.pick?.pin_ineligible === true}
              selected={selected.includes(t.id)}
              onpin={(tid) => pin(tid)}
              onselect={(tid) => (selected = selectForDuel(selected, tid))}
              onchanged={changed}
            />
          {/each}
        </div>
        {#if view.takes_total > view.takes.length}<p class="muted">Showing {view.takes.length} of {view.takes_total} takes.</p>{/if}
      </section>
    {/if}

    {#if (view.proposals ?? []).length > 0}
      <section aria-labelledby="prop-h">
        <h2 id="prop-h">Proposals</h2>
        <ProposalList proposals={view.proposals} canDecide={human} {titleOf} onaccept={accept} ondismiss={dismiss} />
      </section>
    {/if}

    <PickHistory songId={s.id} events={view.events ?? []} />

    {#if view.lineage}
      <p><a href="/lineage/{encodeURIComponent(view.lineage.root_track_id)}" data-testid="song-lineage">Lineage · {view.lineage.generations} generations</a></p>
    {/if}
  </article>
{/if}

<style>
  header {
    margin-bottom: 1rem;
  }
  header h1 {
    display: inline-block;
    margin-right: 0.5rem;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
  }
  .chip,
  .state {
    padding: 0 0.5rem;
    border: 1px solid #5a4d43;
    border-radius: 0.6rem;
    font-size: 0.8rem;
    color: inherit;
    text-decoration: none;
  }
  .cards {
    display: grid;
    gap: 0.6rem;
  }
  .edit {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: end;
  }
  section {
    margin: 1rem 0;
  }
</style>

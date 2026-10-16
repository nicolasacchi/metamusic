<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import SongChip from "../lib/components/SongChip.svelte";
  import { summaryToChip } from "../lib/songView";
  import type { Viewer } from "../lib/genIntent";
  import { fetchViewer } from "../lib/viewer";

  type Detail = components["schemas"]["MuseDetail"];
  interface Props {
    id: string;
    navigate?: (to: string, replace?: boolean) => void;
    initial?: Detail;
    viewer?: Viewer | null;
  }
  let { id, initial, viewer = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
  const human = $derived(me !== null && me.kind === "human");

  // svelte-ignore state_referenced_locally
  let detail = $state<Detail | null>(initial ?? null);
  let error = $state("");
  let note = $state("");
  let editing = $state(false);
  let titleDraft = $state("");
  let artistDraft = $state("");
  let briefDraft = $state("");
  let seq = 0;

  async function load() {
    const mine = ++seq;
    try {
      const r = await api.GET("/api/muses/{id}", { params: { path: { id }, query: { songs_limit: 200 } } });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.response.status === 404 ? "Muse not found" : (r.error?.message ?? "Failed to load the Muse");
        return;
      }
      detail = r.data;
    } catch {
      if (mine === seq) error = "Failed to load the Muse";
    }
  }
  $effect(() => {
    void id;
    if (!initial) {
      detail = null;
      error = "";
      void load();
    }
  });

  async function patch(body: Omit<components["schemas"]["UpdateMuseInBody"], "revision">): Promise<boolean> {
    if (!detail) return false;
    note = "";
    try {
      const r = await api.PATCH("/api/muses/{id}", { params: { path: { id } }, body: { ...body, revision: detail.muse.revision } });
      if (!r.data) {
        note = r.response.status === 409 ? "Someone else changed this Muse; reloaded." : (r.error?.message ?? "Could not save the change");
        if (r.response.status === 409) await load();
        return false;
      }
      await load();
      return true;
    } catch {
      note = "Could not save the change";
      return false;
    }
  }
  function startEdit() {
    if (!detail) return;
    titleDraft = detail.muse.title;
    artistDraft = detail.muse.artist ?? "";
    briefDraft = detail.muse.brief ?? "";
    editing = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    if (!detail) return;
    const m = detail.muse;
    const body: Omit<components["schemas"]["UpdateMuseInBody"], "revision"> = {};
    if (titleDraft.trim() && titleDraft.trim() !== m.title) body.title = titleDraft.trim();
    if (m.kind === "reference" && artistDraft.trim() && artistDraft.trim() !== (m.artist ?? "")) body.artist = artistDraft.trim();
    if (m.kind === "brief" && briefDraft.trim() && briefDraft.trim() !== (m.brief ?? "")) body.brief = briefDraft.trim();
    if (Object.keys(body).length === 0 || (await patch(body))) editing = false;
  }
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a href="/board">Back to the Board</a></p>
{:else if !detail}
  <p class="muted">Loading…</p>
{:else}
  {@const m = detail.muse}
  <article data-testid="muse-page" data-muse-id={m.id}>
    <header>
      {#if editing}
        <form class="edit" onsubmit={save}>
          <label>Title <input bind:value={titleDraft} maxlength="200" required data-testid="muse-title-input" /></label>
          {#if m.kind === "reference"}
            <label>Artist <input bind:value={artistDraft} maxlength="200" data-testid="muse-artist-input" /></label>
          {:else}
            <label>Brief <textarea bind:value={briefDraft} maxlength="2000" data-testid="muse-brief-input"></textarea></label>
          {/if}
          <button type="submit" data-testid="muse-save">Save</button>
          <button type="button" onclick={() => (editing = false)}>Cancel</button>
        </form>
      {:else}
        <h1 data-testid="muse-title">{m.title}</h1>
        {#if m.kind === "reference" && m.artist}<p class="muted" data-testid="muse-artist">{m.artist}</p>{/if}
        {#if m.kind === "brief" && m.brief}<p data-testid="muse-brief">{m.brief}</p>{/if}
        {#if human}
          <button type="button" onclick={startEdit} data-testid="muse-edit">Edit</button>
          <button type="button" aria-pressed={m.hidden} onclick={() => patch({ hidden: !m.hidden })} data-testid="muse-hide">{m.hidden ? "Unhide" : "Hide"}</button>
        {/if}
      {/if}
    </header>
    {#if note}<p role="alert" data-testid="muse-note">{note}</p>{/if}

    <p class="cov" data-testid="muse-coverage">
      <span>{detail.coverage.songs} songs</span>
      <span>{detail.coverage.draft} draft</span>
      <span>{detail.coverage.rendered} rendered</span>
      <span>{detail.coverage.judged} judged</span>
      <span>{detail.coverage.picked} picked</span>
      {#if detail.coverage.queued_jobs > 0}<span>{detail.coverage.queued_jobs} queued</span>{/if}
    </p>

    {#if detail.angles.length > 0}
      <p class="angles" data-testid="muse-angles">
        {#each detail.angles as a (a.angle ?? "")}
          <a href="/board?muse={encodeURIComponent(m.id)}&angle={encodeURIComponent(a.angle ?? "")}" class="pill">{a.angle ?? "(none)"} <span class="muted">{a.songs}</span></a>
        {/each}
      </p>
    {/if}

    <section aria-labelledby="muse-songs-h">
      <h2 id="muse-songs-h">Songs <span class="muted">({detail.songs_total})</span></h2>
      <div class="songs">
        {#each detail.songs as s (s.id)}
          <SongChip chip={summaryToChip(s)} href="/songs/{encodeURIComponent(s.id)}" />
        {/each}
      </div>
      {#if detail.songs_total > detail.songs.length}<p class="muted">Showing {detail.songs.length} of {detail.songs_total}.</p>{/if}
    </section>
    <p><a href="/board?muse={encodeURIComponent(m.id)}" data-testid="muse-board-link">Show on the Board</a></p>
  </article>
{/if}

<style>
  .cov,
  .angles,
  .songs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
  }
  .pill {
    padding: 0 0.5rem;
    border: 1px solid #5a4d43;
    border-radius: 0.6rem;
    font-size: 0.85rem;
    color: inherit;
    text-decoration: none;
  }
  .edit {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: end;
  }
</style>

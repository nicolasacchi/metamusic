<script lang="ts">
  import { untrack } from "svelte";
  import { routeReady } from "../lib/scrollRestore";
  import { api } from "../api/client";
  import AddToCrate from "../lib/components/AddToCrate.svelte";
  import AddToRecord from "../lib/components/AddToRecord.svelte";
  import CrateMembers from "../lib/components/CrateMembers.svelte";
  import FilterBuilder from "../lib/components/FilterBuilder.svelte";
  import { getCrate, updateCrate, type CrateDetail, type Failure } from "../lib/crateApi";
  import { filterToQuery, type CrateFilter, type FilterErrors } from "../lib/crateFilter";
  import type { Viewer } from "../lib/genIntent";
  import { fetchViewer } from "../lib/viewer";

  interface Props {
    id: string;
    /** Test seam: a loaded Crate without fetching. */
    initial?: CrateDetail;
    viewer?: Viewer | null;
  }
  let { id, initial, viewer = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let detail = $state<CrateDetail | null>(initial ?? null);
  let error = $state("");
  let note = $state("");
  let stale = $state(false);
  let version = $state(0);
  let seq = 0;
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
  const human = $derived(me !== null && me.kind === "human");

  async function load() {
    const mine = ++seq;
    error = "";
    const r = await getCrate(api, id);
    if (mine !== seq) return;
    if (r.ok) {
      detail = r.value;
      stale = false;
    } else error = r.message;
    void routeReady();
  }
  $effect(() => {
    void id;
    untrack(() => {
      if (!initial) void load();
      else void routeReady();
    });
  });

  const crate = $derived(detail?.crate ?? null);
  const smart = $derived(crate?.kind === "smart");

  // ---- members (manual) and the picks the entry points use ----
  let loadedIds = $state<{ songIds: string[]; ids: string[]; total: number }>({ songIds: [], ids: [], total: 0 });
  function onloaded(total: number, rows: { id: string; songId: string | null }[]) {
    loadedIds = { ids: rows.map((r) => r.id), songIds: [...new Set(rows.flatMap((r) => (r.songId ? [r.songId] : [])))], total };
  }
  let menu = $state<"" | "record" | "crate">("");

  // ---- metadata ----
  let editing = $state(false);
  let nameDraft = $state("");
  let busy = $state(false);

  function fail(r: Failure) {
    note = r.message;
    stale = r.stale;
  }
  async function patch(p: Parameters<typeof updateCrate>[3]): Promise<boolean> {
    if (!crate || busy) return false;
    busy = true;
    note = "";
    const r = await updateCrate(api, crate.id, crate.revision, p);
    busy = false;
    if (r.ok) {
      detail = r.value;
      stale = false;
      version++;
      return true;
    }
    fail(r);
    return false;
  }
  async function saveName(e: Event) {
    e.preventDefault();
    const n = nameDraft.trim();
    if (!crate || !n || n === crate.name) {
      editing = false;
      return;
    }
    if (await patch({ name: n })) editing = false;
  }

  // ---- smart: the draft filter ----
  let draft = $state<CrateFilter>({});
  let errors = $state<FilterErrors>({});
  const saved = $derived(JSON.stringify(crate?.filter ?? {}));
  const dirty = $derived(JSON.stringify(draft) !== saved && detail !== null);
  const bad = $derived(Object.keys(errors).length > 0);
  const previewQuery = $derived(bad ? filterToQuery({}) : filterToQuery(draft));
  async function saveFilter() {
    if (bad || !dirty) return;
    if (await patch({ filter: draft })) note = "Saved.";
  }
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a href="/crates">Back to Crates</a></p>
{:else if !crate || !detail}
  <p class="muted">Loading…</p>
{:else}
  <article data-testid="crate-page" data-id={crate.id}>
    <header>
      {#if editing}
        <form class="edit" onsubmit={saveName}>
          <label>Name <input bind:value={nameDraft} maxlength="80" required data-testid="crate-name-input" /></label>
          <button type="submit" data-testid="crate-name-save">Save</button>
          <button type="button" onclick={() => (editing = false)}>Cancel</button>
        </form>
      {:else}
        <h1 data-testid="crate-title">{crate.name}</h1>
        <button
          type="button"
          onclick={() => {
            nameDraft = crate.name;
            editing = true;
          }}
          data-testid="crate-rename">Rename</button>
      {/if}
      <p class="chips">
        <span class="chip" data-testid="crate-kind">{crate.kind}</span>
        <span class="chip">{crate.subject === "song" ? "Songs" : "Takes"}</span>
        {#if crate.hidden}<span class="chip">hidden</span>{/if}
        <span data-testid="crate-count"><strong>{detail.count}</strong> {crate.subject === "song" ? (detail.count === 1 ? "Song" : "Songs") : detail.count === 1 ? "Take" : "Takes"}</span>
        <button type="button" aria-pressed={crate.hidden} disabled={busy} onclick={() => patch({ hidden: !crate.hidden })} data-testid="crate-hide">{crate.hidden ? "Unhide" : "Hide"}</button>
      </p>
      {#if crate.description}<p class="muted">{crate.description}</p>{/if}
    </header>

    {#if stale}
      <p role="alert" class="banner" data-testid="crate-stale">
        Changed elsewhere.
        <button type="button" onclick={() => void load()} data-testid="crate-reload">Reload</button>
      </p>
    {/if}
    {#if note && !stale}<p role="status" data-testid="crate-note">{note}</p>{/if}
    {#if detail.warnings.length > 0}
      <ul class="warnings" data-testid="crate-warnings">
        {#each detail.warnings as w (w)}<li role="status">{w}</li>{/each}
      </ul>
    {/if}

    {#if smart}
      <section aria-labelledby="filter-h">
        <h2 id="filter-h">Filter</h2>
        {#key crate.revision}
          <FilterBuilder subject={crate.subject} value={crate.filter ?? {}} onchange={(f, e) => ((draft = f), (errors = e))} />
        {/key}
        <p>
          <button type="button" class="primary" disabled={busy || bad || !dirty} onclick={saveFilter} data-testid="crate-save-filter">Save filter</button>
        </p>
      </section>
      <section aria-labelledby="preview-h">
        <h2 id="preview-h">Preview</h2>
        <CrateMembers subject={crate.subject} query={previewQuery} {onloaded} />
      </section>
    {:else}
      <section aria-labelledby="members-h">
        <h2 id="members-h">Members</h2>
        <CrateMembers subject={crate.subject} query={{ crate_id: crate.id }} removeFrom={crate.id} {version} {onloaded} onremoved={() => void load()} />
      </section>
    {/if}

    {#if loadedIds.ids.length > 0}
      <section class="entry" aria-labelledby="use-h">
        <h2 id="use-h">Use these {crate.subject === "song" ? "Songs" : "Takes"}</h2>
        <p class="muted">{loadedIds.ids.length < loadedIds.total ? `The ${loadedIds.ids.length} loaded of ${loadedIds.total}: load more to include the rest.` : `All ${loadedIds.total}.`}</p>
        <div class="row">
          <button type="button" aria-expanded={menu === "record"} onclick={() => (menu = menu === "record" ? "" : "record")} data-testid="crate-to-record">Add to Record…</button>
          {#if !smart}<button type="button" aria-expanded={menu === "crate"} onclick={() => (menu = menu === "crate" ? "" : "crate")} data-testid="crate-to-crate">Add to Crate…</button>{/if}
        </div>
        {#if menu === "record"}<AddToRecord songIds={loadedIds.songIds} {human} heading="Add to Record" />{/if}
        {#if menu === "crate"}<AddToCrate subject={crate.subject} ids={loadedIds.ids} heading="Add to another Crate" />{/if}
      </section>
    {/if}
  </article>
{/if}

<style>
  header h1 {
    display: inline-block;
    margin-right: var(--sp-2);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  .chip {
    padding: 0 var(--sp-2);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
  }
  .edit,
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: end;
  }
  .banner {
    padding: var(--sp-2) var(--sp-3);
    background: var(--warn-soft);
    border: 1px solid var(--warn);
    border-radius: var(--r-md);
  }
  .warnings {
    list-style: none;
    margin: var(--sp-2) 0;
    padding: var(--sp-2) var(--sp-3);
    background: var(--warn-soft);
    border: 1px solid var(--warn);
    border-radius: var(--r-md);
  }
  section {
    margin: var(--sp-4) 0;
  }
</style>

<script lang="ts">
  // /diction (More > Library tools): the Diction Lab. The lexicon is every sung word the recogniser was asked about, risky ones first:
  // how often it was heard as another word, what it was heard as, and where. A word can be ignored (it stops warning the lyric lint)
  // and restored. Calibration shows how many lines were marked in the karaoke view. Everything here is a suspicion, not a verdict.
  import { api } from "../api/client";
  import { calibrationText, getLexicon, heardText, lexiconQuery, rateText, updateLexiconEntry, type LexiconEntry, type LexiconFilters, type LexiconPage } from "../lib/dictionApi";
  import { can } from "../lib/me.svelte";
  import { fmt } from "../lib/time";

  interface Props {
    /** Pre-loaded page (unit tests). */
    initial?: LexiconPage;
  }
  let { initial }: Props = $props();

  const LANGS = ["it", "en", "es", "fr", "de", "pt", "und"];
  let filters = $state<LexiconFilters>({ lang: "", risk: "all", q: "" });
  // svelte-ignore state_referenced_locally
  let page = $state<LexiconPage | null>(initial ?? null);
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let message = $state("");
  let rowMessage = $state("");
  let busy = $state<Record<string, boolean>>({});

  const canEdit = $derived(can("update_lexicon_entry"));
  const rk = (e: Pick<LexiconEntry, "lang" | "word">) => `${e.lang}/${e.word}`;

  $effect(() => {
    if (initial) return;
    const f = { ...filters };
    let live = true;
    loading = true;
    message = "";
    const t = setTimeout(() => {
      void getLexicon(api, lexiconQuery(f)).then((r) => {
        if (!live) return;
        if (r.ok) page = r.value;
        else message = r.message;
        loading = false;
      });
    }, 200);
    return () => {
      live = false;
      clearTimeout(t);
    };
  });

  async function more() {
    const cur = page;
    if (!cur?.next_cursor || loading) return;
    loading = true;
    const r = await getLexicon(api, { ...lexiconQuery(filters), cursor: cur.next_cursor });
    if (r.ok) page = { ...r.value, entries: [...cur.entries, ...r.value.entries] };
    else message = r.message;
    loading = false;
  }

  async function setStatus(e: LexiconEntry, status: "active" | "ignored") {
    if (busy[rk(e)] || !page) return;
    busy[rk(e)] = true;
    rowMessage = "";
    const r = await updateLexiconEntry(api, e.lang, e.word, { status });
    busy[rk(e)] = false;
    if (!r.ok) {
      rowMessage = r.message;
      return;
    }
    const cur = page;
    if (cur) page = { ...cur, entries: cur.entries.map((x) => (rk(x) === rk(e) ? r.value : x)) };
  }
</script>

<h1>Diction Lab</h1>
<p class="muted">
  Sung words the recogniser was asked about, risky ones first. A word is risky when it was heard as something else at least half the time (three tries or more). These are suspicions from a recogniser that can mishear sung words.
</p>

{#if page}
  <p class="muted" data-testid="calibration">{calibrationText(page.calibration)}</p>
{/if}

<form class="filters" onsubmit={(e) => e.preventDefault()} aria-label="Lexicon filters">
  <label>Language
    <select bind:value={filters.lang} data-testid="lex-lang">
      <option value="">All</option>
      {#each LANGS as l (l)}<option value={l}>{l}</option>{/each}
    </select>
  </label>
  <label>Show
    <select bind:value={filters.risk} data-testid="lex-risk">
      <option value="all">All words</option>
      <option value="risky">Risky only</option>
      <option value="other">Not risky</option>
    </select>
  </label>
  <label>Word contains
    <input type="search" bind:value={filters.q} placeholder="pan" data-testid="lex-q" />
  </label>
</form>

{#if message}
  <p role="alert">{message}</p>
{:else if loading && !page}
  <p class="muted">Loading…</p>
{:else if page}
  <p class="muted" data-testid="lex-total">{page.total} {page.total === 1 ? "word" : "words"}</p>
  {#if page.entries.length === 0}
    <p class="muted" data-testid="lex-empty">No words yet. They appear once takes have a listening check.</p>
  {:else}
    <ul class="entries" data-testid="lexicon">
      {#each page.entries as e (rk(e))}
        <li class="entry" class:risk={e.risk} class:ignored={e.status === "ignored"} data-testid="lex-entry" data-word={e.word} data-risk={e.risk ? "true" : "false"} data-status={e.status}>
          <div class="top">
            <strong class="word">{e.word}</strong>
            <span class="muted">{e.lang}</span>
            {#if e.risk}<span class="chip risk" data-testid="lex-risk-chip">risky</span>{/if}
            {#if e.status === "ignored"}<span class="chip">ignored</span>{/if}
          </div>
          <p class="muted">{rateText(e)}{e.dropped > 0 ? `, ${e.dropped} not heard` : ""} · {e.tracks} {e.tracks === 1 ? "take" : "takes"}</p>
          {#if e.typical_heard.length > 0}<p class="muted">Heard as {heardText(e.typical_heard)}</p>{/if}
          {#if e.note}<p class="note">{e.note}</p>{/if}
          {#if e.examples.length > 0}
            <p class="muted ex">
              Where:
              {#each e.examples as x, i (i)}{i > 0 ? " · " : " "}<a href="/tracks/{x.track_id}?t={Math.floor(x.at_second)}">{fmt(x.at_second)}</a>{x.heard ? ` (${x.heard})` : ""}{/each}
            </p>
          {/if}
          {#if canEdit}
            {#if e.status === "active"}
              <button type="button" disabled={busy[rk(e)]} onclick={() => setStatus(e, "ignored")} data-testid="lex-ignore">Ignore</button>
            {:else}
              <button type="button" disabled={busy[rk(e)]} onclick={() => setStatus(e, "active")} data-testid="lex-restore">Restore</button>
            {/if}
          {/if}
        </li>
      {/each}
    </ul>
    {#if rowMessage}<p role="alert" data-testid="lex-error">{rowMessage}</p>{/if}
    {#if page.next_cursor}
      <button type="button" disabled={loading} onclick={more} data-testid="lex-more">Show more words</button>
    {/if}
  {/if}
{/if}

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    margin: var(--sp-3) 0;
  }
  label {
    display: grid;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
  }
  .entries {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .entry {
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
  }
  .entry.risk {
    border-color: var(--warn);
  }
  .entry.ignored {
    opacity: 0.7;
  }
  .top {
    display: flex;
    gap: var(--sp-2);
    align-items: baseline;
    flex-wrap: wrap;
  }
  .word {
    font-size: var(--fs-lg);
  }
  p {
    margin: var(--sp-1) 0;
  }
  .chip {
    font-size: var(--fs-xs);
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    background: var(--surface-2);
  }
  .chip.risk {
    background: var(--warn-soft);
    color: var(--warn);
  }
  button {
    min-height: 2rem;
  }
  @media (pointer: coarse) {
    button {
      min-height: var(--hit);
    }
  }
</style>

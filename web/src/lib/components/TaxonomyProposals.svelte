<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { acceptAllBody, decideErrorText, impactText, mappingText, rejectBody, resultText, selectionBody } from "../proposalView";

  type Proposal = components["schemas"]["TaxonomyProposal"];
  type Page = components["schemas"]["TaxonomyProposalsOutBody"];
  type DecideBody = components["schemas"]["DecideTaxonomyInBody"];

  interface Props {
    /** Only the operator decides; everyone else reads. */
    canDecide?: boolean;
    /** Test seam: a pre-loaded page (no fetching). */
    initial?: Page | null;
  }
  let { canDecide = false, initial = null }: Props = $props();

  type View = "open" | "history";
  const HISTORY = ["accepted", "rejected", "expired"] as const;
  const PAGE = 20;

  let view = $state<View>("open");
  // svelte-ignore state_referenced_locally
  let items = $state<Proposal[]>(initial?.items ?? []);
  // svelte-ignore state_referenced_locally
  let next = $state<string | null>(initial?.next_cursor ?? null);
  // svelte-ignore state_referenced_locally
  let total = $state(initial?.total ?? 0);
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let error = $state("");
  /** Unchecked mapping indices per proposal (everything starts checked). */
  let unchecked = $state<Record<string, number[]>>({});
  let notes = $state<Record<string, string>>({});
  let busy = $state<Record<string, boolean>>({});
  let refreshing = $state<Record<string, boolean>>({});
  let outcomes = $state<{ id: string; text: string; error: boolean }[]>([]);
  let seq = 0;

  const stateQuery = (v: View) => (v === "open" ? (["open"] as const) : ([...HISTORY] as ("accepted" | "rejected" | "expired")[]));

  async function load(more = false) {
    const mine = ++seq;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/taxonomy-proposals", {
        params: { query: { state: [...stateQuery(view)], limit: PAGE, ...(more && next ? { cursor: next } : {}) } },
      });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load the proposals";
        return;
      }
      const have = new Set(more ? items.map((x) => x.id) : []);
      items = more ? [...items, ...r.data.items.filter((x) => !have.has(x.id))] : r.data.items;
      next = r.data.next_cursor;
      total = r.data.total;
    } catch {
      if (mine === seq) error = "Failed to load the proposals";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  function setView(v: View) {
    if (v === view) return;
    view = v;
    outcomes = [];
    void load();
  }

  $effect(() => {
    if (initial) return;
    void load();
  });

  const isOn = (p: Proposal, i: number) => !(unchecked[p.id] ?? []).includes(i);
  const selected = (p: Proposal): Set<number> => new Set(p.mappings.map((_, i) => i).filter((i) => isOn(p, i)));
  function toggle(p: Proposal, i: number) {
    const off = unchecked[p.id] ?? [];
    unchecked = { ...unchecked, [p.id]: off.includes(i) ? off.filter((x) => x !== i) : [...off, i] };
  }

  async function refreshImpact(p: Proposal) {
    refreshing = { ...refreshing, [p.id]: true };
    try {
      const r = await api.GET("/api/taxonomy-proposals", { params: { query: { state: ["open"], include_impact: true, limit: 10 } } });
      const fresh = r.data?.items.find((x) => x.id === p.id);
      if (fresh) items = items.map((x) => (x.id === p.id ? { ...x, impact: fresh.impact } : x));
      else if (!r.data) error = r.error?.message ?? "Failed to refresh the impact";
    } catch {
      error = "Failed to refresh the impact";
    } finally {
      refreshing = { ...refreshing, [p.id]: false };
    }
  }

  async function decide(p: Proposal, body: DecideBody | null) {
    if (!body || busy[p.id]) return;
    busy = { ...busy, [p.id]: true };
    try {
      const r = await api.POST("/api/taxonomy-proposals/{id}/decision", { params: { path: { id: p.id } }, body });
      if (r.data) {
        outcomes = [{ id: p.id, text: resultText(r.data), error: false }, ...outcomes.filter((o) => o.id !== p.id)];
      } else {
        outcomes = [{ id: p.id, text: decideErrorText(r.error?.code, r.error?.message), error: r.error?.code !== "TAXONOMY_STALE" }, ...outcomes.filter((o) => o.id !== p.id)];
      }
      await load();
    } catch {
      outcomes = [{ id: p.id, text: "Network error; try again.", error: true }, ...outcomes.filter((o) => o.id !== p.id)];
    } finally {
      busy = { ...busy, [p.id]: false };
    }
  }

  const proposer = (p: Proposal) => p.proposed_by.name || p.proposed_by.id;
</script>

<section aria-label="Taxonomy proposals" aria-busy={loading} data-testid="taxonomy-proposals">
  <div class="views" role="group" aria-label="Proposal state">
    <button type="button" aria-pressed={view === "open"} data-testid="proposals-open" onclick={() => setView("open")}>Open</button>
    <button type="button" aria-pressed={view === "history"} data-testid="proposals-history" onclick={() => setView("history")}>History</button>
    <span class="muted" data-testid="proposals-total">{total}</span>
  </div>
  {#if !canDecide}<p class="muted" data-testid="proposals-readonly">You can read proposals here; the operator decides them.</p>{/if}
  {#if error}<p role="alert" data-testid="proposals-error">{error}</p>{/if}
  {#each outcomes as o (o.id)}
    <p role={o.error ? "alert" : "status"} class:bad={o.error} data-testid="proposal-outcome">{o.text}</p>
  {/each}

  {#if !loading && items.length === 0}
    <p class="muted" data-testid="proposals-empty">{view === "open" ? "No open proposals." : "No decided proposals yet."}</p>
  {/if}

  {#each items as p (p.id)}
    {@const sel = selected(p)}
    {@const open = p.state === "open"}
    <article class="card" data-testid="proposal-card" data-state={p.state}>
      <header>
        <strong data-testid="proposal-agent">{proposer(p)}</strong>
        <span class="state" data-testid="proposal-state">{p.state}</span>
      </header>
      <blockquote data-testid="proposal-rationale">{p.rationale}</blockquote>

      <table aria-label="Mappings">
        <thead>
          <tr>
            {#if canDecide && open}<th scope="col" class="pick">Apply</th>{/if}
            <th scope="col">Mapping</th>
          </tr>
        </thead>
        <tbody>
          {#each p.mappings as m, i (i)}
            <tr data-testid="proposal-mapping">
              {#if canDecide && open}
                <td class="pick">
                  <label class="hit">
                    <input type="checkbox" checked={isOn(p, i)} onchange={() => toggle(p, i)} data-testid="proposal-check" aria-label="Apply {mappingText(m)}" />
                  </label>
                </td>
              {/if}
              <td>
                {mappingText(m)}
                {#if p.accepted?.includes(i)}<span class="tag ok">applied</span>{/if}
                {#if p.skipped?.some((s) => s.index === i)}<span class="tag">skipped: stale</span>{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>

      <p class="impact" data-testid="proposal-impact">{impactText(p.impact)}</p>
      <p class="muted small">Computed at taxonomy version {p.impact.taxonomy_version}; the proposal was made against version {p.base_version}.</p>
      {#if p.decided_note}<p class="muted small" data-testid="proposal-note">Note: {p.decided_note}</p>{/if}

      {#if open}
        <div class="acts">
          <button type="button" disabled={refreshing[p.id]} data-testid="proposal-refresh" onclick={() => void refreshImpact(p)}>Refresh impact</button>
          {#if canDecide}
            <label class="note">
              Note (optional)
              <input type="text" maxlength="300" bind:value={notes[p.id]} data-testid="proposal-note-input" autocomplete="off" />
            </label>
            <button type="button" class="primary" disabled={busy[p.id] || sel.size === 0} data-testid="proposal-accept-selected" onclick={() => void decide(p, selectionBody(sel, p.mappings.length, notes[p.id]))}>
              Accept selected ({sel.size})
            </button>
            <button type="button" disabled={busy[p.id]} data-testid="proposal-accept" onclick={() => void decide(p, acceptAllBody(notes[p.id]))}>Accept all</button>
            <button type="button" class="danger" disabled={busy[p.id]} data-testid="proposal-reject" onclick={() => void decide(p, rejectBody(notes[p.id]))}>Reject</button>
          {/if}
        </div>
      {/if}
    </article>
  {/each}

  {#if next}
    <button type="button" class="more" disabled={loading} data-testid="proposals-more" onclick={() => void load(true)}>Load more</button>
  {/if}
</section>

<style>
  .views {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--sp-2);
    margin: var(--sp-2) 0;
  }
  button {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    cursor: pointer;
  }
  button[aria-pressed="true"] {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .primary {
    background: var(--accent);
    color: var(--accent-ink);
    border-color: var(--accent);
    font-weight: var(--fw-bold);
  }
  .danger {
    border-color: var(--bad);
    color: var(--bad);
  }
  .muted {
    color: var(--text-2);
  }
  .small {
    font-size: var(--fs-sm);
    margin: var(--sp-1) 0;
  }
  .bad {
    color: var(--bad);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-3);
    margin: var(--sp-3) 0;
  }
  header {
    display: flex;
    justify-content: space-between;
    gap: var(--sp-2);
  }
  .state {
    color: var(--text-2);
    text-transform: uppercase;
    font-size: var(--fs-sm);
    letter-spacing: var(--tracking-label);
  }
  blockquote {
    margin: var(--sp-2) 0;
    padding-left: var(--sp-3);
    border-left: 2px solid var(--line-strong);
    overflow-wrap: anywhere;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: var(--sp-1) var(--sp-2);
    border-bottom: 1px solid var(--line);
    overflow-wrap: anywhere;
  }
  .pick {
    width: var(--hit);
  }
  .hit {
    display: flex;
    align-items: center;
    justify-content: center;
    min-width: var(--hit);
    min-height: var(--hit);
  }
  .hit input {
    width: 1.25rem;
    height: 1.25rem;
  }
  .tag {
    margin-left: var(--sp-2);
    padding: 0 var(--sp-2);
    border-radius: var(--r-md);
    background: var(--warn-soft);
    color: var(--warn);
    font-size: var(--fs-sm);
  }
  .tag.ok {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .impact {
    margin: var(--sp-3) 0 var(--sp-1);
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: end;
    margin-top: var(--sp-3);
  }
  .note {
    display: grid;
    flex: 1 1 12rem;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .note input {
    min-height: var(--hit);
  }
  .more {
    margin-top: var(--sp-3);
  }
</style>

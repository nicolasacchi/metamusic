<script lang="ts">
  // The effective term mapping, the top unmapped terms with an inline "Map…" form, and the version history (C100.5).
  import type { components } from "../../api/schema";
  import { FACET_LABELS, mappingOptions, validateMapping, type Facet, type MappableFacet } from "../soundsView";

  type Item = components["schemas"]["TaxonomyItem"];
  type Version = components["schemas"]["TaxonomyVersion"];
  type Unmapped = components["schemas"]["UnmappedTerm"];
  interface Props {
    items: readonly Item[];
    unmapped: readonly Unmapped[];
    versions: readonly Version[];
    currentVersion: number;
    /** Mapping is a write operation. */
    canMap: boolean;
    /** Resolves to "" on success or the message to show. */
    onmap: (m: { term: string; facet: MappableFacet; value: string }) => Promise<string>;
  }
  let { items, unmapped, versions, currentVersion, canMap, onmap }: Props = $props();

  const options = mappingOptions();
  let open = $state<string | null>(null);
  let facet = $state<MappableFacet>("genre_family");
  let value = $state("");
  let message = $state("");
  let busy = $state(false);

  function toggle(term: string) {
    open = open === term ? null : term;
    value = "";
    message = "";
  }
  const hint = $derived(options.find((o) => o.facet === facet)?.hint ?? "");

  async function submit(e: SubmitEvent, term: string) {
    e.preventDefault();
    const bad = validateMapping({ term, facet, value });
    if (bad) {
      message = bad;
      return;
    }
    busy = true;
    message = "";
    try {
      message = await onmap({ term, facet, value });
      if (message === "") open = null;
    } finally {
      busy = false;
    }
  }
</script>

<section aria-label="Unmapped terms" data-testid="unmapped">
  <h2>Unmapped terms</h2>
  {#if unmapped.length === 0}
    <p class="muted">Every style term used by a Song is mapped.</p>
  {:else}
    <ul class="unmapped">
      {#each unmapped as u (u.term)}
        <li data-testid="unmapped-term">
          <span class="term">{u.term}</span> <span class="muted">{u.songs} {u.songs === 1 ? "song" : "songs"}</span>
          {#if canMap}<button type="button" data-testid="map-open" aria-expanded={open === u.term} onclick={() => toggle(u.term)}>Map…</button>{/if}
          {#if open === u.term}
            <form class="map" data-testid="map-form" onsubmit={(e) => submit(e, u.term)}>
              <label>
                Facet
                <select bind:value={facet} data-testid="map-facet">
                  {#each options as o (o.facet)}<option value={o.facet}>{o.label}</option>{/each}
                </select>
              </label>
              <label>
                Value
                <input bind:value placeholder={hint} maxlength="40" autocomplete="off" data-testid="map-value" />
              </label>
              <button type="submit" disabled={busy} data-testid="map-submit">Map</button>
              {#if message}<span role="alert" class="err" data-testid="map-error">{message}</span>{/if}
            </form>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<section aria-label="Mapping" data-testid="mapping">
  <h2>Mapping <span class="muted">version {currentVersion}</span></h2>
  <table>
    <thead>
      <tr><th scope="col">Term</th><th scope="col">Facet</th><th scope="col">Value</th><th scope="col" class="num">Since</th></tr>
    </thead>
    <tbody>
      {#each items as it (it.term + "|" + it.facet + "|" + it.value)}
        <tr data-testid="mapping-row">
          <td>{it.term}</td>
          <td>{FACET_LABELS[it.facet as Facet] ?? it.facet}</td>
          <td>{it.value}</td>
          <td class="num">v{it.since_version}</td>
        </tr>
      {:else}
        <tr><td colspan="4" class="muted">No mappings match.</td></tr>
      {/each}
    </tbody>
  </table>
</section>

<section aria-label="Versions" data-testid="versions">
  <h2>Versions</h2>
  <ul>
    {#each versions as v (v.version)}
      <li>v{v.version} · {v.kind}{v.seed_id ? ` ${v.seed_id}` : ""} · {v.created_at.slice(0, 10)}{v.note ? ` · ${v.note}` : ""}</li>
    {/each}
  </ul>
</section>

<style>
  .unmapped {
    list-style: none;
    padding: 0;
  }
  .unmapped li {
    margin: 0.3rem 0;
  }
  .term {
    font-weight: 600;
  }
  .map {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: end;
    margin: 0.3rem 0 0.6rem 1rem;
  }
  .map label {
    display: grid;
    font-size: 0.8rem;
  }
  .err {
    color: #e07070;
  }
  table {
    border-collapse: collapse;
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: 0.2rem 0.5rem;
    border-bottom: 1px solid #3a322b;
  }
  .num {
    text-align: right;
  }
</style>

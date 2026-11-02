<script lang="ts">
  import { fetchAdherence, type Adherence } from "../atlasApi";
  import { api } from "../../api/client";

  interface Props {
    /** Test seam: a report to show without fetching. */
    initial?: Adherence | null;
  }
  let { initial = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let data = $state<Adherence | null>(initial);
  let error = $state("");
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let octave = $state(false);

  async function load() {
    loading = true;
    error = "";
    const r = await fetchAdherence(api, octave ? { octave: true } : {});
    loading = false;
    if (r.ok) data = r.value;
    else error = r.message;
  }
  $effect(() => {
    void octave;
    if (initial) return;
    void load();
  });

  const pct = (n: number) => `${Math.round(n * 100)}%`;
  const bpm = (n: number) => String(Math.round(n * 10) / 10);
  const summary = $derived(
    data
      ? data.summary.n === 0
        ? "No Songs have both a requested and a measured tempo yet."
        : `${data.summary.n} Songs: median tempo error ${bpm(data.summary.median_error_pct)}%, ${pct(data.summary.within_5pct_share)} within 5%, ${pct(data.summary.octave_share)} an octave off.`
      : "",
  );
</script>

<section data-testid="atlas-adherence" aria-label="Requested versus got">
  {#if error}<p role="alert">{error}</p>{/if}
  {#if loading && !data}
    <p class="muted">Loading…</p>
  {:else if data}
    <p data-testid="adherence-summary">{summary}</p>
    <p class="muted">Coverage: {data.coverage.with_pair} of {data.coverage.songs} Songs have both tempos; {data.coverage.with_duration} have a duration.</p>
    <label class="check"><input type="checkbox" bind:checked={octave} /> Octave errors only</label>
    {#if data.by_facet.length > 0}
      <h3>By facet</h3>
      <div class="scroll">
        <table data-testid="adherence-facets">
          <thead><tr><th scope="col">Facet</th><th scope="col">Value</th><th scope="col">Songs</th><th scope="col">Median error</th><th scope="col">Within 5%</th><th scope="col">Octave</th><th scope="col">Duration ratio</th></tr></thead>
          <tbody>
            {#each data.by_facet as f (f.facet + ":" + f.value)}
              <tr>
                <td>{f.facet}</td>
                <td>{f.value}</td>
                <td>{f.n}</td>
                <td>{f.bpm ? bpm(f.bpm.median_error_pct) + "%" : "–"}</td>
                <td>{f.bpm ? pct(f.bpm.within_5pct_share) : "–"}</td>
                <td>{f.bpm ? pct(f.bpm.octave_share) : "–"}</td>
                <td>{f.duration ? bpm(f.duration.median_ratio) : "–"}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    <h3>Songs</h3>
    <div class="scroll">
      <table data-testid="adherence-items">
        <thead><tr><th scope="col">Song</th><th scope="col">Requested</th><th scope="col">Got</th><th scope="col">Error</th><th scope="col">Octave</th></tr></thead>
        <tbody>
          {#each data.items as it (it.take_id)}
            <tr data-testid="adherence-item">
              <th scope="row"><a href="/songs/{encodeURIComponent(it.song_id)}">{it.title}</a></th>
              <td>{bpm(it.requested_bpm)}</td>
              <td>{bpm(it.measured_bpm)}</td>
              <td>{bpm(it.error_pct)}%</td>
              <td>{it.octave ? "yes" : ""}</td>
            </tr>
          {:else}
            <tr><td colspan="5" class="muted">Nothing to show.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if data.next_cursor}<p class="muted">More Songs are not shown here.</p>{/if}
  {/if}
</section>

<style>
  h3 {
    margin: var(--sp-4) 0 var(--sp-2);
    font-size: var(--fs-md);
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: var(--fs-sm);
  }
  th,
  td {
    text-align: left;
    padding: 0 var(--sp-3);
    height: var(--hit);
    border-bottom: 1px solid var(--line);
    white-space: nowrap;
  }
  thead th {
    color: var(--text-2);
    font-weight: var(--fw-medium);
  }
  tbody th {
    font-weight: var(--fw-regular);
  }
  a {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    color: var(--accent);
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
  }
  .check input {
    width: 24px;
    height: 24px;
  }
</style>

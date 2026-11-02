<script lang="ts">
  import { type AtlasPoint, type AxisPair, type LegendEntry, tableRows } from "../atlasView";
  import Pager from "./Pager.svelte";

  interface Props {
    points: readonly AtlasPoint[];
    axes: AxisPair;
    legend: readonly LegendEntry[];
    selected?: ReadonlySet<string>;
    pageSize?: number;
    ontoggle: (id: string) => void;
    onopen?: (id: string) => void;
  }
  let { points, axes, legend, selected = new Set<string>(), pageSize = 50, ontoggle, onopen }: Props = $props();

  let page = $state(0);
  const offset = $derived(Math.min(page, Math.max(0, Math.ceil(points.length / pageSize) - 1)) * pageSize);
  const rows = $derived(tableRows(points.slice(offset, offset + pageSize), axes, legend));
</script>

<div class="wrap" data-testid="atlas-table">
  <table>
    <caption class="sr">Songs on the Atlas, {points.length} in all</caption>
    <thead>
      <tr><th scope="col">Select</th><th scope="col">Song</th><th scope="col">{axes.x.name}</th><th scope="col">{axes.y.name}</th><th scope="col">Basis</th><th scope="col">State</th><th scope="col">Pick</th><th scope="col">Colour</th></tr>
    </thead>
    <tbody>
      {#each rows as r (r.id)}
        <tr data-testid="atlas-table-row" data-song-id={r.id}>
          <td><input type="checkbox" aria-label="Select {r.title}" checked={selected.has(r.id)} onchange={() => ontoggle(r.id)} /></td>
          <th scope="row"><a href="/songs/{r.id}" onclick={(e) => { if (onopen && !e.metaKey && !e.ctrlKey) { e.preventDefault(); onopen(r.id); } }}>{r.title}</a></th>
          <td>{r.x}</td>
          <td>{r.y}</td>
          <td>{r.basis}</td>
          <td>{r.state}</td>
          <td>{r.pick}</td>
          <td>{r.colour}</td>
        </tr>
      {:else}
        <tr><td colspan="8" class="muted">No points to show.</td></tr>
      {/each}
    </tbody>
  </table>
  {#if points.length > pageSize}
    <Pager page={Math.floor(offset / pageSize)} {offset} count={rows.length} total={points.length} hasNext={offset + pageSize < points.length} onprev={() => (page = Math.max(0, page - 1))} onnext={() => (page += 1)} />
  {/if}
</div>

<style>
  .wrap {
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
  input[type="checkbox"] {
    width: 24px;
    height: 24px;
    margin: 10px;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
</style>

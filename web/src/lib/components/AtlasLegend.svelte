<script lang="ts">
  import { legendEntries, type LegendEntry } from "../atlasView";
  import { PREDICTED_FOOTNOTE } from "../atlasApi";

  interface Props {
    legend: readonly LegendEntry[];
    colorBy: string;
    /** The colour index highlighted by a click (the others dim); null for none. */
    focus: number | null;
    onfocus: (c: number | null) => void;
  }
  let { legend, colorBy, focus, onfocus }: Props = $props();
  const rows = $derived(legendEntries(legend));
</script>

<section class="legend" data-testid="atlas-legend" aria-label="Legend">
  {#if rows.length > 0}
    <ul>
      {#each rows as r (r.c)}
        <li>
          <button type="button" aria-pressed={focus === r.c} onclick={() => onfocus(focus === r.c ? null : r.c)} data-testid="atlas-legend-entry">
            <span class="sw" style:background={r.cssVar} aria-hidden="true"></span>
            <span class="v">{r.value}</span>
            <span class="n">{r.count}</span>
          </button>
        </li>
      {/each}
    </ul>
  {:else if colorBy && colorBy !== "none"}
    <p class="muted">Nothing to colour.</p>
  {/if}
  <p class="muted note" data-testid="atlas-footnote">{PREDICTED_FOOTNOTE}</p>
</section>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-1) var(--sp-2);
  }
  button {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
    padding: 0 var(--sp-3);
    border-radius: 1rem;
    font-size: var(--fs-sm);
  }
  button[aria-pressed="true"] {
    background: var(--accent-soft);
    outline: 1px solid var(--accent);
  }
  .sw {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    flex: none;
  }
  .n {
    color: var(--text-2);
  }
  .note {
    font-size: var(--fs-xs);
    margin: var(--sp-2) 0 0;
  }
</style>

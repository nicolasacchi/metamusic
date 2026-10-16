<script lang="ts">
  // One facet of the Sounds browser: its values with Song counts; a click toggles the selection (C100.5).
  import type { FacetBlock } from "../soundsView";

  interface Props {
    block: FacetBlock;
    /** `facet:value` selections. */
    selected: readonly string[];
    ontoggle: (facet: string, value: string) => void;
  }
  let { block, selected, ontoggle }: Props = $props();
</script>

<section class="facet" aria-label={block.label} data-testid="facet-{block.facet}">
  <h3>{block.label}</h3>
  {#if block.values.length === 0}
    <p class="muted">No values yet.</p>
  {:else}
    <ul>
      {#each block.values as v (v.value)}
        {@const on = selected.includes(`${block.facet}:${v.value}`)}
        <li>
          <button type="button" class="val" aria-pressed={on} data-testid="facet-value" data-facet={block.facet} data-value={v.value} onclick={() => ontoggle(block.facet, v.value)}>
            <span class="name">{v.value}</span>
            <span class="count" data-testid="facet-count">{v.count}</span>
            {#if v.mismatches}<span class="mm" title="requested and measured tempo disagree">⚠ {v.mismatches}</span>{/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .facet h3 {
    margin: 0 0 0.3rem;
    font-size: 0.95rem;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }
  .val {
    display: inline-flex;
    gap: 0.35rem;
    align-items: center;
    min-height: 2rem;
    padding: 0.1rem 0.55rem;
    border: 1px solid #5a4d43;
    border-radius: 1rem;
    background: transparent;
    color: inherit;
    font: inherit;
    font-size: 0.85rem;
    cursor: pointer;
  }
  @media (pointer: coarse) {
    .val {
      min-height: 44px;
    }
  }
  .val[aria-pressed="true"] {
    background: #3a322b;
    border-color: #f2a65a;
  }
  .count {
    opacity: 0.75;
    font-size: 0.78rem;
  }
  .mm {
    color: #e0a030;
    font-size: 0.75rem;
  }
</style>

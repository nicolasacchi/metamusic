<script lang="ts">
  import type { Chip, GridModel, GridRow, Selection } from "../boardView";
  import SongChip from "./SongChip.svelte";

  interface Props {
    grid: GridModel;
    selection: Selection;
    onchip: (row: GridRow, chip: Chip, e: MouseEvent) => void;
    onmore?: (row: GridRow) => void;
    loadingMore?: boolean;
  }
  let { grid, selection, onchip, onmore, loadingMore = false }: Props = $props();
</script>

<div class="scroll">
  <div role="grid" aria-label="Muse Board" aria-colcount={grid.columns.length + 1} style:--cols={grid.columns.length} data-testid="board-grid">
    <div role="row" class="head">
      <div role="columnheader" class="muse">Muse</div>
      {#each grid.columns as c (c.angle ?? "\u0000none")}
        <div role="columnheader" class="col" data-testid="board-column">{c.label}</div>
      {/each}
    </div>
    {#each grid.rows as row (row.key)}
      <div role="row" class="row" data-testid={row.muse ? "board-row" : "board-unassigned"}>
        <div role="rowheader" class="muse">
          {#if row.muse}<a href="/muses/{row.muse.id}">{row.label}</a>{:else}<strong>{row.label}</strong>{/if}
          <div class="counts muted">
            {row.songsTotal} songs · {row.counts.draft} draft · {row.counts.rendered} rendered · {row.counts.judged} judged · {row.counts.picked} picked{#if row.counts.queued > 0} · {row.counts.queued} queued{/if}
          </div>
        </div>
        {#each row.cells as cell (cell.angle ?? "\u0000none")}
          <div role="gridcell" class="cell">
            {#each cell.chips as chip (chip.id)}
              <SongChip {chip} selected={selection.ids.has(chip.id)} onclick={(e) => onchip(row, chip, e)} />
            {/each}
          </div>
        {/each}
        {#if row.chipsMore && onmore}
          <div class="more">
            <button type="button" disabled={loadingMore} onclick={() => onmore(row)} data-testid="unassigned-more">Show more</button>
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .scroll {
    overflow-x: auto;
  }
  [role="grid"] {
    display: grid;
    min-width: max-content;
  }
  .head,
  .row {
    display: grid;
    grid-template-columns: 14rem repeat(var(--cols, 1), minmax(12rem, 1fr));
    border-bottom: 1px solid #3a322b;
  }
  .muse {
    padding: 0.4rem 0.5rem;
    position: sticky;
    left: 0;
    background: #14110f;
  }
  .col {
    padding: 0.4rem 0.5rem;
    font-weight: 600;
  }
  .cell {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    align-content: flex-start;
    padding: 0.4rem 0.5rem;
  }
  .counts {
    font-size: 0.75rem;
  }
  .more {
    grid-column: 1 / -1;
    padding: 0.4rem 0.5rem;
  }
</style>

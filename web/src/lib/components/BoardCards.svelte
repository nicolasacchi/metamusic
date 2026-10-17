<script lang="ts">
  import type { Chip, GridModel, GridRow, Selection } from "../boardView";
  import SongChip from "./SongChip.svelte";

  interface Props {
    grid: GridModel;
    selection: Selection;
    /** Select mode: chips toggle a checkbox state instead of opening the Song. */
    selecting: boolean;
    onchip: (row: GridRow, chip: Chip, e: MouseEvent) => void;
    onmore?: (row: GridRow) => void;
    loadingMore?: boolean;
  }
  let { grid, selection, selecting, onchip, onmore, loadingMore = false }: Props = $props();
</script>

<div class="cards" data-testid="board-cards">
  {#each grid.rows as row (row.key)}
    <section class="card" data-testid={row.muse ? "board-row" : "board-unassigned"} aria-label={row.label}>
      <header>
        {#if row.muse}<a class="name" href="/muses/{row.muse.id}">{row.label}</a>{:else}<strong class="name">{row.label} ({row.songsTotal})</strong>{/if}
        <div class="counts muted">
          {row.songsTotal} songs · {row.counts.draft} draft · {row.counts.rendered} rendered · {row.counts.judged} judged · {row.counts.picked} picked{#if row.counts.queued > 0} · {row.counts.queued} queued{/if}
        </div>
      </header>
      {#each row.cells.filter((c) => c.chips.length > 0) as cell (cell.angle ?? "\u0000none")}
        <div class="angle">
          <span class="label" data-testid="board-angle">{cell.angle ?? "(none)"}</span>
          <div class="chips">
            {#each cell.chips as chip (chip.id)}
              {#if selecting}
                <SongChip {chip} selected={selection.ids.has(chip.id)} onclick={(e) => onchip(row, chip, e)} />
              {:else}
                <SongChip {chip} href="/songs/{chip.id}" />
              {/if}
            {/each}
          </div>
        </div>
      {/each}
      {#if row.chipsMore && onmore}
        <button type="button" disabled={loadingMore} onclick={() => onmore(row)} data-testid="unassigned-more">Show more</button>
      {/if}
    </section>
  {/each}
</div>

<style>
  .cards {
    display: grid;
    gap: var(--sp-3);
  }
  .card {
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-3);
    background: var(--bg-raised);
  }
  .name {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    font-weight: var(--fw-bold);
    font-size: var(--fs-md);
    overflow-wrap: anywhere;
  }
  .counts {
    font-size: var(--fs-xs);
  }
  .angle {
    margin-top: var(--sp-2);
  }
  .label {
    display: block;
    font-size: var(--fs-xs);
    font-weight: var(--fw-bold);
    text-transform: uppercase;
    letter-spacing: var(--tracking-label);
    color: var(--text-3);
    margin-bottom: var(--sp-1);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
</style>

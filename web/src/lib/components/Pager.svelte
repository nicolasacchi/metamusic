<script lang="ts">
  interface Props {
    page: number;
    /** Absolute index of the first item on this page. */
    offset: number;
    /** Items on this page (may be short even when a next page exists). */
    count: number;
    total: number;
    hasNext: boolean;
    busy?: boolean;
    onprev: () => void;
    onnext: () => void;
  }
  let { page, offset, count, total, hasNext, busy = false, onprev, onnext }: Props = $props();
  const from = $derived(count === 0 ? 0 : offset + 1);
  const to = $derived(offset + count);
</script>

<nav class="pager" aria-label="Pagination" data-testid="pager">
  <button type="button" onclick={onprev} disabled={busy || page === 0} aria-label="Previous page">Previous</button>
  <span class="muted">{from}–{to} of {total}</span>
  <button type="button" onclick={onnext} disabled={busy || !hasNext} aria-label="Next page">Next</button>
</nav>

<style>
  .pager {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    margin: 0.75rem 0;
  }
</style>

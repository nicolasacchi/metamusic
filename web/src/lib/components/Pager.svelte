<script lang="ts">
  interface Props {
    page: number;
    pageSize: number;
    total: number;
    hasNext: boolean;
    onprev: () => void;
    onnext: () => void;
  }
  let { page, pageSize, total, hasNext, onprev, onnext }: Props = $props();
  const from = $derived(total === 0 ? 0 : page * pageSize + 1);
  const to = $derived(Math.min(total, (page + 1) * pageSize));
</script>

<nav class="pager" aria-label="Pagination" data-testid="pager">
  <button type="button" onclick={onprev} disabled={page === 0}>Previous</button>
  <span class="muted">{from}–{to} of {total}</span>
  <button type="button" onclick={onnext} disabled={!hasNext}>Next</button>
</nav>

<style>
  .pager {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    margin: 0.75rem 0;
  }
</style>

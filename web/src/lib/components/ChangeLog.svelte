<script lang="ts">
  import type { components } from "../../api/schema";

  type Change = components["schemas"]["ChangeView"];
  interface Props {
    changes: readonly Change[];
    /** Only this slot's rows when set. */
    slotId?: string | null;
    hasMore?: boolean;
    busy?: boolean;
    /** The Record's changelog may lag by one sync pass. */
    pending?: boolean;
    onmore?: () => void;
    onclearfilter?: () => void;
  }
  let { changes, slotId = null, hasMore = false, busy = false, pending = false, onmore, onclearfilter }: Props = $props();
  const rows = $derived(slotId ? changes.filter((c) => c.slot_id === slotId) : changes);
  const when = (iso: string) => iso.replace("T", " ").replace(/(\.\d+)?Z$/, " UTC");
</script>

<section aria-label="Changelog" data-testid="record-changelog">
  {#if pending}<p class="muted" role="status">Catching up with Pick changes…</p>{/if}
  {#if slotId}
    <p class="muted">Showing one slot. <button type="button" onclick={onclearfilter}>Show all</button></p>
  {/if}
  {#if rows.length === 0}
    <p class="muted">No changes yet.</p>
  {:else}
    <ol>
      {#each rows as c (c.seq)}
        <li data-testid="record-changelog-row" data-kind={c.kind}>
          <span class="text">{c.text}</span>
          <span class="meta muted">
            {#if c.from}<a href="/tracks/{c.from.id}" class="tap">from {c.from.title}</a>{/if}
            {#if c.to}<a href="/tracks/{c.to.id}" class="tap">to {c.to.title}</a>{/if}
            <time datetime={c.at}>{when(c.at)}</time> · {c.actor.name}
          </span>
        </li>
      {/each}
    </ol>
  {/if}
  {#if hasMore}<button type="button" disabled={busy} onclick={onmore} data-testid="record-changelog-more">Older changes</button>{/if}
</section>

<style>
  ol {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: var(--sp-2) 0;
    border-bottom: 1px solid var(--line);
  }
  .text {
    display: block;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-1) var(--sp-3);
    font-size: var(--fs-sm);
  }
</style>

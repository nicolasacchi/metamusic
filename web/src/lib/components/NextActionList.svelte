<script lang="ts">
  import { shortList, type NextActionItem } from "../featuresView";

  interface Props {
    /** null while loading. */
    items: NextActionItem[] | null;
    testid?: string;
  }
  let { items, testid = "next-actions" }: Props = $props();
  const list = $derived(shortList(items ?? []));
</script>

<section aria-labelledby="{testid}-h" data-testid={testid}>
  <h2 id="{testid}-h">What next</h2>
  {#if items === null}
    <p class="muted">Loading…</p>
  {:else if list.shown.length === 0}
    <p class="muted" data-testid="{testid}-empty">Nothing suggested right now.</p>
  {:else}
    <ul>
      {#each list.shown as a, i (i)}
        <li data-code={a.code}>{a.text}</li>
      {/each}
    </ul>
    {#if list.more > 0}<p class="muted">and {list.more} more</p>{/if}
  {/if}
</section>

<style>
  ul {
    margin: 0;
    padding-left: var(--sp-5);
    display: grid;
    gap: var(--sp-2);
  }
</style>

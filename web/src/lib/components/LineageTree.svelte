<script lang="ts">
  // Pre-order tree rows (C84.5). Node actions: open, Variant, A/B with parent, Compare.
  import type { components } from "../../api/schema";
  import { lineageRows } from "../lineageTree";
  import { formatMicroUSD } from "../money";
  import { openAbWithParent, pushNavigate } from "../abParent";
  import { toggled, type Viewer } from "../genIntent";
  import GenerateButton from "./GenerateButton.svelte";
  import TrackBadges from "./TrackBadges.svelte";

  type Node = components["schemas"]["LineageNode"];
  interface Props {
    nodes: Node[];
    /** The track whose page opened the tree; highlighted. */
    currentId?: string;
    me: Viewer | null;
    navigate?: (to: string) => void;
  }
  let { nodes, currentId = "", me, navigate }: Props = $props();

  let collapsed = $state<Set<string>>(new Set());
  let busy = $state("");
  let error = $state("");
  const rows = $derived(lineageRows(nodes, collapsed));

  function toggle(id: string) {
    collapsed = toggled(collapsed, id, !collapsed.has(id));
  }
  async function ab(parent: string, child: string) {
    if (busy) return;
    busy = child;
    error = (await openAbWithParent(parent, child, navigate ?? pushNavigate)) || "";
    busy = "";
  }
  function stars(n: number | null): string {
    return n === null ? "" : "★".repeat(n);
  }
</script>

<ol class="tree" data-testid="lineage-tree">
  {#each rows as r (r.node.id)}
    {@const x = r.node}
    <li class:current={x.id === currentId} data-testid="lineage-node" data-id={x.id}>
      <span class="glyph" aria-hidden="true">{r.connector}</span>
      {#if r.hasChildren}
        <button type="button" class="fold" aria-expanded={!r.collapsed} aria-label={r.collapsed ? "Expand" : "Collapse"} onclick={() => toggle(x.id)}>{r.collapsed ? "+" : "−"}</button>
      {/if}
      <a href="/tracks/{x.id}" data-testid="lineage-open">{x.title}</a>
      {#if x.operator}<span class="chip" data-testid="lineage-operator">{x.operator.replaceAll("_", " ")}</span>{/if}
      {#if x.inferred}<span class="chip" data-testid="lineage-inferred" title="Inferred from matching recipes">inferred</span>{/if}
      <TrackBadges recipeKnown={x.recipe_known} lossy={x.lossy} />
      <span class="muted">{x.status}</span>
      {#if x.stars !== null}<span title="Your rating">{stars(x.stars)}</span>{/if}
      {#if x.cost}<span class="muted" data-testid="lineage-cost">{formatMicroUSD(x.cost.display_micro_usd)}</span>{/if}
      {#if r.collapsed}<span class="muted">(+{r.hidden} hidden)</span>{/if}
      <span class="actions">
        <GenerateButton trackId={x.id} recipeKnown={x.recipe_known} lossy={x.lossy} {me} go={navigate ?? pushNavigate} />
        {#if x.parent_id}
          <button type="button" disabled={busy === x.id} onclick={() => void ab(x.parent_id ?? "", x.id)} data-testid="lineage-ab-parent">A/B with parent</button>
          <a href="/compare?a={x.parent_id}&b={x.id}" data-testid="lineage-compare">Compare</a>
        {/if}
      </span>
    </li>
  {/each}
</ol>
{#if error}<p role="alert">{error}</p>{/if}

<style>
  .tree {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: 0.2rem 0.3rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
  }
  li.current {
    background: #2e2823;
  }
  .glyph {
    font-family: ui-monospace, monospace;
    white-space: pre;
    color: #8a7f74;
  }
  .fold {
    padding: 0 0.4rem;
    line-height: 1.2;
  }
  .chip {
    font-size: 0.75rem;
    padding: 0 0.5rem;
    border-radius: 0.6rem;
    background: #3a322b;
  }
  .actions {
    display: inline-flex;
    gap: 0.4rem;
    align-items: center;
    margin-left: auto;
  }
</style>

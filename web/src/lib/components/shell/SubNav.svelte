<script lang="ts">
  import { subActive, subLinks, type Tab } from "../../nav";

  interface Props {
    tab: Tab;
    routeName: string | null | undefined;
  }
  let { tab, routeName }: Props = $props();
  const links = $derived(subLinks(tab));
</script>

{#if links.length > 0}
  <nav class="sub" aria-label="{tab === 'library' ? 'Library' : 'Judge'} views" data-testid="subnav">
    {#each links as l (l.path)}
      {@const on = subActive(l, routeName)}
      <a href={l.path} class:on aria-current={on ? "page" : undefined}>{l.label}</a>
    {/each}
  </nav>
{/if}

<style>
  .sub {
    display: flex;
    gap: var(--sp-2);
    margin-bottom: var(--sp-3);
    overflow-x: auto;
  }
  a {
    display: inline-flex;
    align-items: center;
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--line);
    border-radius: var(--r-pill);
    color: var(--text-2);
    text-decoration: none;
    font-size: var(--fs-sm);
    white-space: nowrap;
  }
  a.on {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent);
    font-weight: var(--fw-bold);
  }
</style>

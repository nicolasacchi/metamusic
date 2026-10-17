<script lang="ts">
  import { TABS, tabFor, type Tab } from "../../nav";
  import InboxIcon from "../icons/InboxIcon.svelte";
  import LibraryIcon from "../icons/LibraryIcon.svelte";
  import QueueIcon from "../icons/QueueIcon.svelte";
  import SwordsIcon from "../icons/SwordsIcon.svelte";
  import MoreIcon from "../icons/MoreIcon.svelte";

  interface Props {
    routeName: string | null | undefined;
    /** Badge text per tab (visible), with an accessible phrase ("3 open duels"). */
    badges: Partial<Record<Tab, { text: string; label: string; testid: string }>>;
  }
  let { routeName, badges }: Props = $props();
  const current = $derived(tabFor(routeName));
  const icons = { new: InboxIcon, library: LibraryIcon, queue: QueueIcon, judge: SwordsIcon, more: MoreIcon };
</script>

<!-- One element: the fixed bottom tab bar on a phone, the single-row top nav on desktop. -->
<nav aria-label="Main" class="tabs" data-testid="tabbar">
  {#each TABS as t (t.id)}
    {@const Icon = icons[t.id]}
    {@const b = badges[t.id]}
    <a href={t.path} class:active={current === t.id} aria-current={current === t.id ? "page" : undefined} data-testid="tab-{t.id}">
      <span class="ico"><Icon size={22} /></span>
      <span class="lbl">{t.label}</span>
      {#if b}
        <span class="badge" aria-hidden="true" data-testid={b.testid}>{b.text}</span>
        <span class="sr">{b.label}</span>
      {/if}
    </a>
  {/each}
</nav>

<style>
  .tabs {
    display: flex;
    gap: var(--sp-1);
  }
  a {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    color: var(--text-2);
    text-decoration: none;
    min-height: var(--hit);
    padding: 0 var(--sp-3);
    border-radius: var(--r-pill);
    font-size: var(--fs-sm);
  }
  a:hover {
    color: var(--text);
  }
  a.active {
    color: var(--accent);
    font-weight: var(--fw-bold);
  }
  .ico {
    display: inline-flex;
  }
  .badge {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    background: var(--accent);
    color: var(--accent-ink);
    font-size: var(--fs-xs);
    font-weight: var(--fw-bold);
    line-height: 1.4;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  @media (min-width: 768px) {
    .tabs {
      margin-left: auto;
    }
  }
  @media (max-width: 767px) {
    .tabs {
      position: fixed;
      left: 0;
      right: 0;
      bottom: 0;
      z-index: var(--z-bar);
      box-sizing: border-box;
      height: calc(var(--tabbar-h) + var(--safe-b));
      padding-bottom: var(--safe-b);
      gap: 0;
      background: var(--bg-raised);
      border-top: 1px solid var(--line);
    }
    a {
      flex: 1;
      min-width: 0;
      flex-direction: column;
      justify-content: center;
      gap: 2px;
      padding: 0;
      border-radius: 0;
      font-size: var(--fs-xs);
    }
    .lbl {
      white-space: nowrap;
    }
    .badge {
      position: absolute;
      top: 4px;
      left: calc(50% + 6px);
      padding: 0 5px;
    }
  }
</style>

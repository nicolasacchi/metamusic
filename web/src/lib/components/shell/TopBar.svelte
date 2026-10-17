<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    title: string;
    /** Phone: a Back button on pages that are not the root of their tab. */
    back?: boolean;
    onback?: () => void;
    /** The tab navigation: inline on desktop, a fixed bottom bar on the phone (CSS). */
    children?: Snippet;
    /** The page is behind a modal sheet. */
    inert?: boolean;
  }
  let { title, back = false, onback, children, inert = false }: Props = $props();
</script>

<header class="topbar" data-testid="topbar" {inert}>
  {#if back}
    <button type="button" class="back" aria-label="Back" onclick={onback}>‹</button>
  {/if}
  <strong class="brand">metamusic</strong>
  <span class="title" data-testid="topbar-title">{title}</span>
  {@render children?.()}
</header>

<style>
  .topbar {
    box-sizing: border-box;
    height: var(--topbar-h);
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    padding: 0 var(--sp-4);
    border-bottom: 1px solid var(--line);
    background: var(--bg);
    overflow: hidden;
  }
  .brand {
    display: none;
  }
  .title {
    flex: 1;
    min-width: 0;
    font-size: var(--fs-lg);
    font-weight: var(--fw-bold);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .back {
    min-width: var(--hit);
    min-height: var(--hit);
    margin-left: calc(-1 * var(--sp-2));
    border: none;
    font-size: 1.75rem;
    line-height: 1;
    padding: 0;
  }
  @media (min-width: 768px) {
    .brand {
      display: block;
    }
    .title,
    .back {
      display: none;
    }
  }
</style>

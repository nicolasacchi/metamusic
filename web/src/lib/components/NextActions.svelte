<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { saveIntent } from "../genIntent";
  import { audienceLabel, groupActions, hintLink, moreText } from "../nextActionsView";

  type Page = components["schemas"]["NextActionsPage"];

  interface Props {
    /** Test seam: a pre-loaded page (no fetching). */
    initial?: Page | null;
  }
  let { initial = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let page = $state<Page | null>(initial);
  let seq = 0;

  const groups = $derived(groupActions(page));
  const count = $derived(page ? page.total : 0);

  /** A failure keeps the previous list: this section never blocks the page. */
  async function load() {
    const mine = ++seq;
    try {
      const r = await api.GET("/api/next-actions", { params: { query: { limit: 20 } } });
      if (mine === seq && r.data) page = r.data;
    } catch {
      // keep what is shown
    }
  }

  $effect(() => {
    if (initial) return;
    void load();
    const onVisible = () => {
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  });
</script>

<section aria-labelledby="next-h" data-testid="next-actions" data-count={count}>
  <h2 id="next-h">What next <span class="count" data-testid="next-actions-count">{count}</span></h2>
  {#each groups as g (g.code)}
    <div class="group" data-code={g.code}>
      <ul>
        {#each g.items as a, i (g.code + ":" + (a.subject.id ?? "") + ":" + i)}
          {@const link = hintLink(a)}
          {@const who = audienceLabel(a)}
          <li class="card" data-testid="next-action-card" data-code={a.code} data-audience={a.audience}>
            {#if link?.kind === "intent"}
              <a class="body" href={link.href} onclick={() => saveIntent(sessionStorage, link.intent)}><span class="text">{a.text}</span></a>
            {:else if link}
              <a class="body" href={link.href}><span class="text">{a.text}</span></a>
            {:else}
              <span class="body"><span class="text">{a.text}</span></span>
            {/if}
            {#if who}<span class="needs" data-testid="next-action-needs">{who}</span>{/if}
          </li>
        {/each}
      </ul>
      {#if g.more > 0}<p class="more" data-testid="next-action-more">{moreText(g.more)}</p>{/if}
    </div>
  {/each}
</section>

<style>
  h2 {
    font-size: var(--fs-sm);
    text-transform: uppercase;
    letter-spacing: var(--tracking-label);
    color: var(--text-2);
    margin: var(--sp-5) 0 var(--sp-2);
  }
  .count {
    margin-left: var(--sp-2);
    color: var(--text-3);
  }
  .group {
    margin-bottom: var(--sp-3);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .card {
    display: flex;
    align-items: stretch;
    gap: var(--sp-2);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
  }
  .body {
    flex: 1;
    display: flex;
    align-items: center;
    min-height: var(--hit);
    padding: var(--sp-2) var(--sp-3);
    color: var(--text);
    text-decoration: none;
    overflow-wrap: anywhere;
  }
  a.body:hover {
    background: var(--surface-2);
  }
  .needs {
    align-self: center;
    margin-right: var(--sp-3);
    padding: 0 var(--sp-2);
    border-radius: var(--r-md);
    background: var(--accent-soft);
    color: var(--accent);
    font-size: var(--fs-sm);
    white-space: nowrap;
  }
  .more {
    margin: var(--sp-1) 0 0;
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
</style>

<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { NAV_LINKS } from "../lib/nav";
    import { offline } from "../lib/offline.svelte";
  import { approvals } from "../lib/approvals.svelte";
  import { badgeText } from "../lib/approvals";
  import LockIcon from "../lib/components/icons/LockIcon.svelte";

  type Whoami = components["schemas"]["WhoamiOutBody"];
  type Ping = components["schemas"]["PingOutBody"];

  let me = $state<Whoami | null>(null);
  let ping = $state<Ping | null>(null);
  let aboutError = $state("");

  $effect(() => {
    void (async () => {
      const [m, p] = await Promise.allSettled([api.GET("/api/me"), api.GET("/api/ping")]);
      if (m.status === "fulfilled" && m.value.data) me = m.value.data;
      if (p.status === "fulfilled" && p.value.data) ping = p.value.data;
      if (!me || !ping) aboutError = "Failed to load";
    })();
  });

  const GROUPS = ["Make", "Library tools", "Settings", "Secure"] as const;
  const rows = (g: string) => NAV_LINKS.filter((l) => l.group === g && (l.path !== "/offline" || offline.available));
  const approvalBadge = $derived(approvals.viewer === "human" ? badgeText(approvals.items.length, approvals.more) : null);
</script>

<h1>More</h1>

{#each GROUPS as g (g)}
  <section aria-labelledby="more-{g.replace(' ', '-')}" data-testid="more-group">
    <h2 id="more-{g.replace(' ', '-')}">{g}</h2>
    <ul>
      {#each rows(g) as l (l.path)}
        <li>
          <a
            href={l.path}
            data-testid={l.testid}
            aria-label={l.testid === "approvals-nav" && approvalBadge ? `Approvals, ${approvalBadge} pending` : undefined}
          >
            <span class="name">{l.label}</span>
            {#if l.path === "/offline" && offline.badge}<span class="badge" data-testid="offline-badge">{offline.badge}</span>{/if}
            {#if l.testid === "approvals-nav" && approvalBadge}<span class="badge" data-testid="approvals-row-badge">{approvalBadge}</span>{/if}
            {#if l.secure}
              <span class="lock" title="Secure page"><LockIcon size={16} /></span>
            {/if}
          </a>
        </li>
      {/each}
    </ul>
  </section>
{/each}

<section aria-labelledby="more-About" data-testid="more-about">
  <h2 id="more-About">About</h2>
  <div class="about" aria-label="Status">
    <span>Identity <b data-testid="identity">{me ? me.name : "…"}</b></span>
    <span>Access <b data-testid="via">{me ? `${me.kind} via ${me.via} (${me.scopes.join(", ")})` : "…"}</b></span>
    <span>Server <b data-testid="version">{ping ? `${ping.ok ? "ok" : "down"} ${ping.version}` : "…"}</b></span>
  </div>
  {#if aboutError}<p role="alert">{aboutError}</p>{/if}
</section>

<style>
  section {
    margin: 0 0 var(--sp-5);
  }
  h2 {
    margin: 0 0 var(--sp-2);
    font-size: var(--fs-xs);
    letter-spacing: var(--tracking-label);
    text-transform: uppercase;
    color: var(--text-3);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  li + li {
    border-top: 1px solid var(--line);
  }
  a {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    color: var(--text);
    text-decoration: none;
    background: var(--surface);
  }
  a:hover {
    background: var(--surface-2);
  }
  .name {
    flex: 1;
  }
  .lock {
    display: inline-flex;
    color: var(--text-2);
  }
  .badge {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    background: var(--accent);
    color: var(--accent-ink);
    font-size: var(--fs-xs);
  }
  .about {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-1) var(--sp-5);
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
</style>

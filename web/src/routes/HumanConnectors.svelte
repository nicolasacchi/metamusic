<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import GrantRow from "../lib/components/GrantRow.svelte";
  import { pushToast } from "../lib/judge.svelte";

  type List = components["schemas"]["ConnectorGrantList"];
  let data = $state<List | null>(null);
  let error = $state("");

  async function load() {
    try {
      const r = await api.GET("/api/connectors", {});
      if (r.data) {
        data = r.data;
        error = "";
      } else error = r.error?.message ?? "Failed to load connectors";
    } catch {
      error = "Failed to load connectors";
    }
  }
  $effect(() => {
    void load();
  });

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      pushToast("Copy failed; select the text and copy manually");
    }
  }
</script>

<h1>Connectors</h1>
<p><a href="/human/subsonic">Subsonic clients</a></p>

{#if error}<p role="alert">{error}</p>{/if}
{#if data}
  {@const o = data.oidc}
  <section aria-label="OIDC status" data-testid="oidc-status">
    {#if o.enabled}
      <p>Sign-in provider: <code>{o.issuer}</code> ({o.alg}, {o.keys.count} key{o.keys.count === 1 ? "" : "s"} cached)</p>
      <p>
        MCP URL for a hosted client: <code data-testid="mcp-url">{o.resource}</code>
        <button type="button" onclick={() => void copy(o.resource)}>Copy</button>
      </p>
      <p class="muted">Required scope <code>{o.required_scope}</code>. Client secrets are never shown here.</p>
    {:else}
      <p class="muted">Hosted connectors are off (no OIDC issuer configured).</p>
    {/if}
  </section>

  {#if data.items.length === 0}
    <p class="muted">No connector grants yet. A grant appears after a hosted client signs in the first time.</p>
  {:else}
    <div class="scroll-x">
      <table data-testid="grant-list">
        <thead><tr><th scope="col">Client</th><th scope="col">Scopes</th><th scope="col">Limits (empty = default)</th><th scope="col">Last used</th><th scope="col">State</th><th scope="col"></th></tr></thead>
        <tbody>
          {#each data.items as g (g.id + g.updated_at)}
            <GrantRow grant={g} onchanged={() => void load()} />
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

<style>
  /* a phone shows a wide table by scrolling it, never by letting cells overlap the buttons of the next column */
  .scroll-x {
    overflow-x: auto;
  }
  .scroll-x :global(td) {
    vertical-align: top;
  }
  .scroll-x :global(button) {
    white-space: nowrap;
  }
</style>

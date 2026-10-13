<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import TokenForm from "../lib/components/TokenForm.svelte";
  import { pushToast } from "../lib/judge.svelte";

  type TokenInfo = components["schemas"]["TokenInfo"];
  let items = $state<TokenInfo[] | null>(null);
  let error = $state("");

  async function load() {
    try {
      const r = await api.GET("/api/tokens", {});
      if (r.data) {
        items = r.data.items;
        error = "";
      } else error = r.error?.message ?? "Failed to load tokens";
    } catch {
      error = "Failed to load tokens";
    }
  }
  $effect(() => {
    void load();
  });

  async function revoke(t: TokenInfo) {
    if (!confirm(`Revoke token "${t.name}"? Anything using it stops working.`)) return;
    try {
      const r = await api.POST("/human/api/tokens/{id}/revoke", { params: { path: { id: t.id } } });
      if (!r.data) pushToast(r.error?.message ?? "Could not revoke the token");
    } catch {
      pushToast("Could not revoke the token");
    }
    await load();
  }
</script>

<h1>Tokens</h1>
<p><a href="/human/devices">Devices</a></p>
<TokenForm oncreated={() => void load()} />

{#if error}<p role="alert">{error}</p>{/if}
{#if items}
  {#if items.length === 0}
    <p class="muted">No tokens yet.</p>
  {:else}
    <table data-testid="token-list">
      <thead><tr><th scope="col">Name</th><th scope="col">Prefix</th><th scope="col">Scopes</th><th scope="col">Last used</th><th scope="col">State</th><th scope="col"></th></tr></thead>
      <tbody>
        {#each items as t (t.id)}
          <tr data-revoked={t.revoked_at !== null}>
            <td>{t.name}</td>
            <td><code>{t.prefix}</code></td>
            <td>{t.scopes.join(", ")}</td>
            <td>{t.last_used_at ?? "never"}</td>
            <td>{t.revoked_at ? `revoked ${t.revoked_at}` : "active"}</td>
            <td>{#if !t.revoked_at}<button type="button" onclick={() => revoke(t)} data-testid="token-revoke">Revoke</button>{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

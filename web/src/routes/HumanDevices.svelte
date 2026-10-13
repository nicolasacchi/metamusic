<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import PairingPanel from "../lib/components/PairingPanel.svelte";
  import { pushToast } from "../lib/judge.svelte";

  type Device = components["schemas"]["Device"];
  let items = $state<Device[] | null>(null);
  let error = $state("");

  async function load() {
    try {
      const r = await api.GET("/human/api/devices", {});
      if (r.data) {
        items = r.data.items;
        error = "";
      } else error = r.error?.message ?? "Failed to load devices";
    } catch {
      error = "Failed to load devices";
    }
  }
  $effect(() => {
    void load();
  });

  async function revoke(d: Device) {
    if (!confirm(`Unpair "${d.name}"?`)) return;
    try {
      const r = await api.POST("/human/api/devices/{id}/revoke", { params: { path: { id: d.id } } });
      if (!r.data) pushToast(r.error?.message ?? "Could not unpair the device");
    } catch {
      pushToast("Could not unpair the device");
    }
    await load();
  }
</script>

<h1>Devices</h1>
<p><a href="/human/tokens">Tokens</a></p>
<PairingPanel oncreated={() => void load()} />

{#if error}<p role="alert">{error}</p>{/if}
{#if items}
  {#if items.length === 0}
    <p class="muted">No paired devices.</p>
  {:else}
    <table data-testid="device-list">
      <thead><tr><th scope="col">Name</th><th scope="col">Paired by</th><th scope="col">Last seen</th><th scope="col">Expires</th><th scope="col">State</th><th scope="col"></th></tr></thead>
      <tbody>
        {#each items as d (d.id)}
          <tr data-revoked={d.revoked_at !== null}>
            <td>{d.name}<br /><span class="muted">{d.user_agent}</span></td>
            <td>{d.paired_by}</td>
            <td>{d.last_seen_at ?? "never"}</td>
            <td>{d.expires_at}</td>
            <td>{d.revoked_at ? `revoked ${d.revoked_at}` : "active"}</td>
            <td>{#if !d.revoked_at}<button type="button" onclick={() => revoke(d)} data-testid="device-revoke">Unpair</button>{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

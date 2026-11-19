<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import SecretOnce from "../lib/components/SecretOnce.svelte";
  import SubsonicClientForm from "../lib/components/SubsonicClientForm.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { can } from "../lib/me.svelte";
  import { actionError, hasOwnerColumn, ownerLabel } from "../lib/deviceView";
  import { createSecretOnce } from "../lib/secretOnce";

  type Client = components["schemas"]["SubsonicClient"];
  let items = $state<Client[] | null>(null);
  let serverUrl = $state("");
  let error = $state("");
  const once = createSecretOnce();
  let secrets = $state<typeof once.secrets>({});
  let shownUser = $state("");

  function forget() {
    once.clear();
    secrets = {};
  }
  // Secrets live in this component only and are dropped on navigation away and on unmount.
  $effect(() => {
    const detach = once.attach(window);
    const sync = () => (secrets = { ...once.secrets });
    window.addEventListener("popstate", sync);
    window.addEventListener("pagehide", sync);
    return () => {
      detach();
      window.removeEventListener("popstate", sync);
      window.removeEventListener("pagehide", sync);
      once.clear();
    };
  });

  async function load() {
    try {
      const r = await api.GET("/human/api/subsonic-clients", {});
      if (r.data) {
        items = r.data.items;
        serverUrl = r.data.server_url;
        error = "";
      } else error = actionError(r.error, "Failed to load clients");
    } catch {
      error = "Failed to load clients";
    }
  }
  $effect(() => {
    void load();
  });

  async function revoke(c: Client) {
    if (!confirm(`Revoke "${c.name}"? The app stops working until you create a new client.`)) return;
    try {
      const r = await api.POST("/human/api/subsonic-clients/{id}/revoke", { params: { path: { id: c.id } } });
      if (!r.data) pushToast(actionError(r.error, "Could not revoke the client"));
    } catch {
      pushToast("Could not revoke the client");
    }
    await load();
  }
</script>

<h1>Subsonic clients</h1>
{#if can("list_connector_grants")}<p><a href="/human/connectors">Connectors</a></p>{/if}
{#if serverUrl}<p>Server URL for the app: <code data-testid="subsonic-server-url">{serverUrl}</code></p>{/if}

<SubsonicClientForm
  oncreated={(r) => {
    once.show(r.secrets);
    secrets = { ...once.secrets };
    shownUser = r.username;
    if (r.serverUrl) serverUrl = r.serverUrl;
    void load();
  }}
/>

{#if secrets.apiKey || secrets.password}
  <SecretOnce {secrets} {serverUrl} username={shownUser} ondismiss={forget} />
{/if}

{#if error}<p role="alert">{error}</p>{/if}
{#if items}
  {@const ownerCol = hasOwnerColumn(items)}
  {#if items.length === 0}
    <p class="muted">No clients yet.</p>
  {:else}
    <div class="scroll-x">
      <table data-testid="subsonic-list">
        <thead><tr><th scope="col">Name</th>{#if ownerCol}<th scope="col">Owner</th>{/if}<th scope="col">Username</th><th scope="col">Credentials</th><th scope="col">Last seen</th><th scope="col">State</th><th scope="col"></th></tr></thead>
        <tbody>
          {#each items as c (c.id)}
            <tr data-revoked={c.revoked_at !== null}>
              <td>{c.name}</td>
              {#if ownerCol}<td data-testid="subsonic-owner">{ownerLabel(c.owner)}</td>{/if}
              <td><code>{c.username}</code></td>
              <td>{[c.has_api_key ? `API key ${c.api_key_prefix ?? ""}…` : "", c.has_password ? "password" : ""].filter(Boolean).join(", ")}</td>
              <td>{c.last_seen_at ?? "never"}{c.last_client ? ` (${c.last_client})` : ""}</td>
              <td>{c.revoked_at ? `revoked ${c.revoked_at}` : "active"}</td>
              <td>{#if !c.revoked_at}<button type="button" onclick={() => revoke(c)} data-testid="subsonic-revoke">Revoke</button>{/if}</td>
            </tr>
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

<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";

  type System = components["schemas"]["SystemOutBody"];

  let info = $state<System | null>(null);
  let error = $state("");

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/system");
        if (r.data) info = r.data;
        else error = "Failed to load system info";
      } catch {
        error = "Failed to load system info";
      }
    })();
  });

  const fields: (keyof System)[] = ["version", "commit", "auth_mode", "schema_version", "started_at", "uptime_s"];
</script>

<h1>Settings</h1>
{#if error}<p role="alert">{error}</p>{/if}
{#if info}
  <dl data-testid="system-info">
    {#each fields as k (k)}
      <dt>{k}</dt>
      <dd>{info[k]}</dd>
    {/each}
  </dl>
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

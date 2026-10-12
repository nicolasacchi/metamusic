<script lang="ts">
  import { api } from "../api/client";

  let info = $state<Record<string, string | number> | null>(null);
  let error = $state("");

  $effect(() => {
    void (async () => {
      const r = await api.GET("/api/system");
      if (r.data) info = r.data as unknown as Record<string, string | number>;
      else error = "Failed to load system info";
    })();
  });
</script>

<h1>Settings</h1>
{#if error}<p role="alert">{error}</p>{/if}
{#if info}
  <dl data-testid="system-info">
    {#each Object.entries(info).filter(([k]) => k !== "$schema") as [k, v] (k)}
      <dt>{k}</dt>
      <dd>{v}</dd>
    {/each}
  </dl>
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

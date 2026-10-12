<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";

  type Whoami = components["schemas"]["WhoamiOutBody"];
  type Ping = components["schemas"]["PingOutBody"];

  let me = $state<Whoami | null>(null);
  let ping = $state<Ping | null>(null);
  let error = $state("");

  $effect(() => {
    void (async () => {
      const [m, p] = await Promise.allSettled([api.GET("/api/me"), api.GET("/api/ping")]);
      if (m.status === "fulfilled" && m.value.data) me = m.value.data;
      if (p.status === "fulfilled" && p.value.data) ping = p.value.data;
      if (m.status === "rejected" || p.status === "rejected" || !me || !ping) error = "Failed to load";
    })();
  });
</script>

<h1>Library</h1>
<p class="muted">Placeholder: tracks will appear here.</p>
{#if error}<p role="alert">{error}</p>{/if}
<dl>
  <dt>Identity</dt>
  <dd data-testid="identity">{me ? me.name : "…"}</dd>
  <dt>Access</dt>
  <dd data-testid="via">{me ? `${me.kind} via ${me.via} (${me.scopes.join(", ")})` : "…"}</dd>
  <dt>Server</dt>
  <dd data-testid="version">{ping ? `${ping.ok ? "ok" : "down"} ${ping.version}` : "…"}</dd>
</dl>

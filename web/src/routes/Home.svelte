<script lang="ts">
  import { api } from "../api/client";

  let me = $state<{ kind: string; name: string; scopes: string[]; via: string } | null>(null);
  let ping = $state<{ ok: boolean; version: string } | null>(null);
  let error = $state("");

  $effect(() => {
    void (async () => {
      const [m, p] = await Promise.all([api.GET("/api/me"), api.GET("/api/ping")]);
      if (m.data) me = m.data;
      if (p.data) ping = p.data;
      if (m.error || p.error) error = "Failed to load";
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

<script lang="ts">
  import { api } from "../../api/client";
  import { idempotentAction } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { createReveal, mcpTemplates } from "../humanView";
  import { pushToast } from "../judge.svelte";

  type Scope = components["schemas"]["CreateTokenInBody"]["scopes"] extends (infer S)[] | null ? S : never;
  const ALL: Scope[] = ["read", "write", "generate", "admin"];

  interface Props {
    oncreated?: () => void;
  }
  let { oncreated }: Props = $props();

  let name = $state("");
  let scopes = $state<Scope[]>(["read", "write"]);
  // Lifetime in days; 0 = never expires. Defaults to the server default (90).
  const TTLS: { days: number; label: string }[] = [
    { days: 30, label: "30 days" },
    { days: 90, label: "90 days (default)" },
    { days: 365, label: "1 year" },
    { days: 0, label: "Never expires" },
  ];
  let ttlDays = $state(90);
  let busy = $state(false);
  let error = $state("");
  let shown = $state("");
  let copied = $state(false);
  const reveal = createReveal();
  const tpl = mcpTemplates(location.origin);

  function forget() {
    reveal.clear();
    shown = "";
    copied = false;
  }
  // The token is held in memory only: it is dropped on any navigation away and on unmount.
  $effect(() => {
    window.addEventListener("popstate", forget);
    window.addEventListener("pagehide", forget);
    return () => {
      window.removeEventListener("popstate", forget);
      window.removeEventListener("pagehide", forget);
      reveal.clear();
    };
  });

  function toggle(s: Scope) {
    scopes = scopes.includes(s) ? scopes.filter((x) => x !== s) : [...scopes, s];
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy) return;
    if (!name.trim() || scopes.length === 0) {
      error = "Name and at least one scope are required";
      return;
    }
    busy = true;
    error = "";
    try {
      const send = idempotentAction((key) =>
        api.POST("/human/api/tokens", { params: { header: { "Idempotency-Key": key } }, body: { name: name.trim(), scopes, ttl_days: ttlDays } }),
      );
      const r = await send();
      if (r.data?.token) {
        reveal.show(r.data.token);
        shown = reveal.token;
        name = "";
        oncreated?.();
      } else error = r.error?.message ?? "Could not create the token";
    } catch {
      error = "Could not create the token";
    } finally {
      busy = false;
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      pushToast("Copy failed; select the text and copy manually");
      return false;
    }
  }
</script>

<form onsubmit={submit} aria-label="New token" data-testid="token-form">
  <label>Name <input bind:value={name} maxlength="80" required data-testid="token-name" /></label>
  <fieldset>
    <legend>Scopes</legend>
    {#each ALL as s (s)}
      <label><input type="checkbox" checked={scopes.includes(s)} onchange={() => toggle(s)} data-testid={`scope-${s}`} /> {s}</label>
    {/each}
  </fieldset>
  <label>Expires
    <select bind:value={ttlDays} data-testid="token-ttl">
      {#each TTLS as o (o.days)}<option value={o.days}>{o.label}</option>{/each}
    </select>
  </label>
  <button type="submit" disabled={busy} data-testid="token-create">Create token</button>
  {#if error}<p role="alert">{error}</p>{/if}
</form>

{#if shown}
  <section aria-label="New token" data-testid="token-reveal">
    <p><strong>Copy this token now; it is shown only once.</strong></p>
    <p>
      <code data-testid="token-value">{shown}</code>
      <button type="button" data-testid="token-copy" onclick={async () => (copied = await copy(shown))}>{copied ? "Copied" : "Copy token"}</button>
      <button type="button" data-testid="token-dismiss" onclick={forget}>Done</button>
    </p>
    <h3>Use it with Claude Code</h3>
    <p class="muted">Set <code>MM_TOKEN</code> in your shell first; the commands below never contain the token.</p>
    <pre data-testid="tpl-claude">{tpl.claude}</pre>
    <button type="button" onclick={() => void copy(tpl.claude)}>Copy command</button>
    <pre data-testid="tpl-stdio">{tpl.stdio}</pre>
    <button type="button" onclick={() => void copy(tpl.stdio)}>Copy command</button>
  </section>
{/if}
